//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePRDevelopmentProviderThreadCaptureOrderAndIntegrity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, firstInput := newPRDevelopmentStoreFixture(t, ":memory:")
	firstRequest := validPRDevelopmentRequestForTest(firstInput)
	first, created, err := store.CapturePRDevelopmentCase(ctx, firstRequest)
	require.NoError(t, err)
	require.True(t, created)

	*clock = clock.Add(time.Minute)
	secondInput := firstInput
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"delivery-thread-second",
		firstInput.WorkflowRef,
		firstInput.WorkflowRevision,
	)
	secondInput.ReviewID = "502"
	secondInput.TriggerReviewNodeID = "PRR_kwDOReview502"
	secondInput.ReviewURL = strings.Replace(
		secondInput.ReviewURL,
		"pullrequestreview-501",
		"pullrequestreview-502",
		1,
	)
	secondInput.Feedback = "Address the second independently submitted review."
	secondInput.ReviewSubmittedAt = secondInput.ReviewSubmittedAt.Add(time.Minute)
	secondRequest := validPRDevelopmentRequestForTest(secondInput)
	second, created, err := store.CapturePRDevelopmentCase(ctx, secondRequest)
	require.NoError(t, err)
	require.True(t, created)

	thread, err := store.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentThreadProvider, thread.Kind)
	assert.Equal(t, firstRequest.Thread, thread.Identity)
	assert.Equal(t, 2, thread.CaseCount)
	require.Len(t, thread.Cases, 2)
	assert.Equal(t, first.ID, thread.Cases[0].CaseID)
	assert.Equal(t, 0, thread.Cases[0].Ordinal)
	assert.Equal(t, second.ID, thread.Cases[1].CaseID)
	assert.Equal(t, 1, thread.Cases[1].Ordinal)
	assert.Equal(t, thread.Cases[0].LinkHash, thread.Cases[1].PreviousHash)
	assert.Equal(t, thread.Cases[1].LinkHash, thread.CasesDigest)

	fromSecond, err := store.GetPRDevelopmentThreadForCase(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, thread, fromSecond)

	retry, created, err := store.CapturePRDevelopmentCase(ctx, secondRequest)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, second, retry)
	afterRetry, err := store.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, afterRetry.CaseCount)

	workbench, err := store.GetPRDevelopmentWorkbench(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.Thread)
	assert.Equal(t, thread.ID, workbench.Thread.ID)
	assert.Equal(t, first.ID, workbench.Thread.Case.CaseID)
	assert.Equal(t, 2, workbench.Thread.CaseCount)

	// Membership readers deliberately do not load sibling feedback. The
	// selected first-case workbench and fixed-size thread membership remain
	// usable, while consuming the corrupted sibling itself fails closed.
	_, err = store.db.Exec(`
		UPDATE pr_development_cases SET feedback = 'corrupted sibling feedback'
		WHERE id = ?`,
		second.ID,
	)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentWorkbench(ctx, first.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentCase(ctx, second.ID)
	require.Error(t, err)
}

func TestStorePRDevelopmentProviderThreadIdentityCollisionsFailClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*PRDevelopmentThreadIdentity)
	}{
		{
			name: "pull ID aliases another repository ID",
			mutate: func(identity *PRDevelopmentThreadIdentity) {
				identity.RepositoryID = "9001"
			},
		},
		{
			name: "repository and number alias another pull ID",
			mutate: func(identity *PRDevelopmentThreadIdentity) {
				identity.PullRequestID = "9002"
			},
		},
		{
			name: "pull author stable ID changes",
			mutate: func(identity *PRDevelopmentThreadIdentity) {
				identity.PullAuthorID = "9003"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, firstInput := newPRDevelopmentStoreFixture(t, ":memory:")
			_, created, err := store.CapturePRDevelopmentCase(
				ctx,
				validPRDevelopmentRequestForTest(firstInput),
			)
			require.NoError(t, err)
			require.True(t, created)

			secondInput := firstInput
			secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
				t,
				store,
				"delivery-thread-collision",
				firstInput.WorkflowRef,
				firstInput.WorkflowRevision,
			)
			request := validPRDevelopmentRequestForTest(secondInput)
			test.mutate(&request.Thread)
			_, created, err = store.CapturePRDevelopmentCase(ctx, request)
			assert.False(t, created)
			assert.ErrorIs(t, err, ErrPRDevelopmentConflict)
			var cases int
			require.NoError(t, store.db.QueryRow(
				`SELECT COUNT(*) FROM pr_development_cases`,
			).Scan(&cases))
			assert.Equal(t, 1, cases)
		})
	}
}

func TestStorePRDevelopmentProviderThreadUsesStableIDsAcrossRepositoryRename(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	firstRequest := validPRDevelopmentRequestForTest(input)
	first, created, err := store.CapturePRDevelopmentCase(ctx, firstRequest)
	require.NoError(t, err)
	require.True(t, created)

	renamed := input
	renamed.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"delivery-thread-renamed-repository",
		input.WorkflowRef,
		input.WorkflowRevision,
	)
	renamed.Repository = "RenamedOrg/RenamedProject"
	renamed.BaseRepository = renamed.Repository
	renamed.PullURL = "https://github.com/RenamedOrg/RenamedProject/pull/42"
	renamed.ReviewURL = renamed.PullURL + "#pullrequestreview-501"
	renamed.PullAuthor = "Renamed-User"
	renamed.TargetUser = "renamed-user"
	renamedRequest := validPRDevelopmentRequestForTest(renamed)
	renamedRequest.Thread = firstRequest.Thread
	second, created, err := store.CapturePRDevelopmentCase(ctx, renamedRequest)
	require.NoError(t, err)
	require.True(t, created)

	thread, err := store.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, firstRequest.Thread, thread.Identity)
	require.Len(t, thread.Cases, 2)
	assert.Equal(t, first.ID, thread.Cases[0].CaseID)
	assert.Equal(t, second.ID, thread.Cases[1].CaseID)
}

func TestStorePRDevelopmentProviderThreadSeparatesDifferentStableIdentity(
	t *testing.T,
) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*PRDevelopmentCaptureInput, *PRDevelopmentThreadIdentity)
	}{
		{
			name: "provider origin",
			mutate: func(
				input *PRDevelopmentCaptureInput,
				identity *PRDevelopmentThreadIdentity,
			) {
				input.PullURL = "https://github.enterprise.example/acme/project/pull/42"
				input.ReviewURL = input.PullURL + "#pullrequestreview-501"
				identity.ProviderOrigin = "https://github.enterprise.example"
			},
		},
		{
			name: "repository and pull IDs",
			mutate: func(
				_ *PRDevelopmentCaptureInput,
				identity *PRDevelopmentThreadIdentity,
			) {
				identity.RepositoryID = "88001"
				identity.PullRequestID = "88002"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
			first, created, err := store.CapturePRDevelopmentCase(
				ctx,
				validPRDevelopmentRequestForTest(input),
			)
			require.NoError(t, err)
			require.True(t, created)

			other := input
			other.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
				t,
				store,
				"delivery-thread-distinct-identity",
				input.WorkflowRef,
				input.WorkflowRevision,
			)
			request := validPRDevelopmentRequestForTest(other)
			test.mutate(&request.Case, &request.Thread)
			second, created, err := store.CapturePRDevelopmentCase(ctx, request)
			require.NoError(t, err)
			require.True(t, created)

			firstThread, err := store.GetPRDevelopmentThreadForCase(ctx, first.ID)
			require.NoError(t, err)
			secondThread, err := store.GetPRDevelopmentThreadForCase(ctx, second.ID)
			require.NoError(t, err)
			assert.NotEqual(t, firstThread.ID, secondThread.ID)
			assert.Equal(t, 1, firstThread.CaseCount)
			assert.Equal(t, 1, secondThread.CaseCount)
		})
	}
}

func TestStorePRDevelopmentCaptureLookupRequiresIntegrityCheckedThreadBinding(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		mutate       func(*testing.T, *Store, PRDevelopmentCase, *PRDevelopmentThreadIdentity)
		wantConflict bool
	}{
		{
			name: "wrong provider identity",
			mutate: func(
				_ *testing.T,
				_ *Store,
				_ PRDevelopmentCase,
				expected *PRDevelopmentThreadIdentity,
			) {
				expected.PullAuthorID = "999999"
			},
			wantConflict: true,
		},
		{
			name: "missing binding",
			mutate: func(
				t *testing.T,
				store *Store,
				developmentCase PRDevelopmentCase,
				_ *PRDevelopmentThreadIdentity,
			) {
				t.Helper()
				_, err := store.db.Exec(`
					DELETE FROM pr_development_thread_cases WHERE case_id = ?`,
					developmentCase.ID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "corrupt binding",
			mutate: func(
				t *testing.T,
				store *Store,
				developmentCase PRDevelopmentCase,
				_ *PRDevelopmentThreadIdentity,
			) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_thread_cases
					SET link_hash = ? WHERE case_id = ?`,
					strings.Repeat("f", 64),
					developmentCase.ID,
				)
				require.NoError(t, err)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
			request := validPRDevelopmentRequestForTest(input)
			developmentCase, created, err := store.CapturePRDevelopmentCase(
				ctx,
				request,
			)
			require.NoError(t, err)
			require.True(t, created)

			loaded, found, err := store.LookupPRDevelopmentCapture(
				ctx,
				input.PRDevelopmentCaptureIdentity,
				&request.Thread,
			)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, developmentCase, loaded)
			_, found, err = store.LookupPRDevelopmentCapture(
				ctx,
				input.PRDevelopmentCaptureIdentity,
				nil,
			)
			assert.False(t, found)
			assert.ErrorIs(t, err, ErrPRDevelopmentConflict)

			expected := request.Thread
			test.mutate(t, store, developmentCase, &expected)
			loaded, found, err = store.LookupPRDevelopmentCapture(
				ctx,
				input.PRDevelopmentCaptureIdentity,
				&expected,
			)
			assert.False(t, found)
			assert.Equal(t, PRDevelopmentCase{}, loaded)
			require.Error(t, err)
			if test.wantConflict {
				assert.ErrorIs(t, err, ErrPRDevelopmentConflict)
			}
		})
	}
}

func TestStoreMigratesV8CasesIntoDistinctLegacyThreads(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-threads.db")
	store, clock, input := newPRDevelopmentStoreFixture(t, path)
	first, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	*clock = clock.Add(time.Minute)
	secondInput := input
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"delivery-legacy-second",
		input.WorkflowRef,
		input.WorkflowRevision,
	)
	second, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(secondInput),
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.Close())

	db := openSchemaTestDB(t, path)
	_, err = db.Exec(`DROP TABLE pr_development_thread_cases`)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE pr_development_threads`)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 8)
	require.NoError(t, db.Close())

	migrated, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	firstThread, err := migrated.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	secondThread, err := migrated.GetPRDevelopmentThreadForCase(ctx, second.ID)
	require.NoError(t, err)
	assert.NotEqual(t, firstThread.ID, secondThread.ID)
	for caseID, thread := range map[string]PRDevelopmentThread{
		first.ID:  firstThread,
		second.ID: secondThread,
	} {
		assert.Equal(t, PRDevelopmentThreadLegacy, thread.Kind)
		assert.Equal(t, PRDevelopmentThreadIdentity{}, thread.Identity)
		assert.Equal(t, caseID, thread.LegacyCaseID)
		assert.Equal(t, 1, thread.CaseCount)
		require.Len(t, thread.Cases, 1)
		assert.Equal(t, caseID, thread.Cases[0].CaseID)
		assert.Zero(t, thread.Cases[0].Ordinal)
	}
	legacyRetry, found, err := migrated.LookupPRDevelopmentCapture(
		ctx,
		input.PRDevelopmentCaptureIdentity,
		nil,
	)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, first, legacyRetry)
	providerThread := validPRDevelopmentThreadForLookupTest(input)
	legacyRetry, found, err = migrated.LookupPRDevelopmentCapture(
		ctx,
		input.PRDevelopmentCaptureIdentity,
		providerThread,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentConflict)
	assert.False(t, found)
	assert.Equal(t, PRDevelopmentCase{}, legacyRetry)
}

func TestStorePRDevelopmentThreadIntegrityCorruptionFailsClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, string)
	}{
		{
			name: "identity hash",
			mutate: func(t *testing.T, store *Store, threadID string) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_threads SET identity_hash = ? WHERE id = ?`,
					strings.Repeat("a", 64),
					threadID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "case count high water",
			mutate: func(t *testing.T, store *Store, threadID string) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_threads SET case_count = 2 WHERE id = ?`,
					threadID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "link and head digest",
			mutate: func(t *testing.T, store *Store, threadID string) {
				t.Helper()
				invalid := strings.Repeat("b", 64)
				_, err := store.db.Exec(`
					UPDATE pr_development_thread_cases SET link_hash = ? WHERE thread_id = ?`,
					invalid,
					threadID,
				)
				require.NoError(t, err)
				_, err = store.db.Exec(`
					UPDATE pr_development_threads SET cases_digest = ? WHERE id = ?`,
					invalid,
					threadID,
				)
				require.NoError(t, err)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
			developmentCase, created, err := store.CapturePRDevelopmentCase(
				ctx,
				validPRDevelopmentRequestForTest(input),
			)
			require.NoError(t, err)
			require.True(t, created)
			thread, err := store.GetPRDevelopmentThreadForCase(ctx, developmentCase.ID)
			require.NoError(t, err)
			test.mutate(t, store, thread.ID)
			_, err = store.GetPRDevelopmentThreadForCase(ctx, developmentCase.ID)
			require.Error(t, err)
			_, err = store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
			require.Error(t, err)
		})
	}
}

func TestStorePRDevelopmentThreadBindingRejectsDetachedSelectedLink(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, input := newPRDevelopmentStoreFixture(t, ":memory:")
	first, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	second := captureAdditionalPRDevelopmentThreadCase(
		t,
		store,
		clock,
		input,
		"delivery-adjacency-second",
		"602",
	)
	_ = captureAdditionalPRDevelopmentThreadCase(
		t,
		store,
		clock,
		input,
		"delivery-adjacency-third",
		"603",
	)
	thread, err := store.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, thread.Cases, 3)
	middle := thread.Cases[1]
	middle.PreviousHash = strings.Repeat("d", 64)
	var captureHash string
	require.NoError(t, store.db.QueryRow(`
		SELECT capture_hash FROM pr_development_cases WHERE id = ?`,
		second.ID,
	).Scan(&captureHash))
	middle.LinkHash, err = extendPRDevelopmentThreadCasesDigest(
		thread.ID,
		thread.IdentityHash,
		captureHash,
		middle,
	)
	require.NoError(t, err)
	_, err = store.db.Exec(`
		UPDATE pr_development_thread_cases
		SET previous_hash = ?, link_hash = ?
		WHERE case_id = ?`,
		middle.PreviousHash,
		middle.LinkHash,
		middle.CaseID,
	)
	require.NoError(t, err)

	// Count/genesis/tail remain unchanged and the selected link is internally
	// self-consistent, but O(1) predecessor/successor checks reject its detached
	// membership without loading either sibling case payload.
	_, err = store.GetPRDevelopmentWorkbench(ctx, second.ID)
	require.Error(t, err)
}

func TestStorePRDevelopmentThreadConcurrentCaptureIsContiguous(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	first, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	requests := make([]PRDevelopmentCaptureRequest, 2)
	for index := range requests {
		candidate := input
		candidate.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
			t,
			store,
			fmt.Sprintf("delivery-concurrent-%d", index),
			input.WorkflowRef,
			input.WorkflowRevision,
		)
		reviewID := fmt.Sprintf("70%d", index+1)
		candidate.ReviewID = reviewID
		candidate.TriggerReviewNodeID = "PRR_concurrent_" + reviewID
		candidate.ReviewURL = candidate.PullURL + "#pullrequestreview-" + reviewID
		candidate.Feedback = "Concurrent review " + reviewID
		requests[index] = validPRDevelopmentRequestForTest(candidate)
	}
	type result struct {
		developmentCase PRDevelopmentCase
		created         bool
		err             error
	}
	results := make(chan result, len(requests))
	var workers sync.WaitGroup
	for index := range requests {
		request := requests[index]
		workers.Add(1)
		go func() {
			defer workers.Done()
			developmentCase, wasCreated, captureErr := store.CapturePRDevelopmentCase(
				ctx,
				request,
			)
			results <- result{
				developmentCase: developmentCase,
				created:         wasCreated,
				err:             captureErr,
			}
		}()
	}
	workers.Wait()
	close(results)
	wantCases := map[string]struct{}{first.ID: {}}
	for captured := range results {
		require.NoError(t, captured.err)
		require.True(t, captured.created)
		wantCases[captured.developmentCase.ID] = struct{}{}
	}
	thread, err := store.GetPRDevelopmentThreadForCase(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, thread.CaseCount)
	require.Len(t, thread.Cases, 3)
	for ordinal, link := range thread.Cases {
		assert.Equal(t, ordinal, link.Ordinal)
		_, found := wantCases[link.CaseID]
		assert.True(t, found)
		delete(wantCases, link.CaseID)
	}
	assert.Empty(t, wantCases)
}

func TestStorePRDevelopmentThreadCaptureRollsBackEveryPartialRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	_, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	_, err = store.db.Exec(`
		CREATE TRIGGER reject_thread_link
		BEFORE INSERT ON pr_development_thread_cases
		BEGIN
			SELECT RAISE(ABORT, 'injected thread-link failure');
		END`)
	require.NoError(t, err)

	candidate := input
	candidate.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"delivery-atomic-failure",
		input.WorkflowRef,
		input.WorkflowRevision,
	)
	candidate.Repository = "acme/other-project"
	candidate.BaseRepository = candidate.Repository
	candidate.PullNumber = 99
	candidate.PullURL = "https://github.com/acme/other-project/pull/99"
	candidate.ReviewURL = candidate.PullURL + "#pullrequestreview-501"
	_, created, err = store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(candidate),
	)
	assert.False(t, created)
	require.Error(t, err)
	for table, want := range map[string]int{
		"pr_development_cases":         1,
		"pr_development_conversations": 1,
		"pr_development_threads":       1,
		"pr_development_thread_cases":  1,
	} {
		var count int
		require.NoError(t, store.db.QueryRow(
			`SELECT COUNT(*) FROM `+table,
		).Scan(&count))
		assert.Equal(t, want, count, table)
	}
}

func TestPRDevelopmentThreadAppendCapacityBoundary(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkPRDevelopmentThreadAppendCapacity(
		MaxPRDevelopmentThreadCases-1,
	))
	err := checkPRDevelopmentThreadAppendCapacity(MaxPRDevelopmentThreadCases)
	assert.ErrorIs(t, err, ErrPRDevelopmentThreadCapacity)
}

func TestStorePRDevelopmentThreadMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "thread-v9-invalid.db")
	db := openSchemaTestDB(t, path)
	installEventingSchemaThroughV7ForRepairTest(t, db)
	_, err := db.Exec(schemaV8)
	require.NoError(t, err)
	malformed := strings.Replace(
		schemaV9PRDevelopmentThreadsTable,
		"case_count >= 1 AND case_count <= 8192",
		"case_count >= 1 AND case_count <= 8193",
		1,
	)
	_, err = db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 8)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v9")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 8, version)
	assert.False(t, schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_thread_cases",
	))
}

func TestStorePRDevelopmentThreadBackfillFailureRollsBackGeneratedState(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "thread-v9-backfill-rollback.db")
	store, _, input := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.Close())

	db := openSchemaTestDB(t, path)
	_, err = db.Exec(`DROP TABLE pr_development_thread_cases`)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE pr_development_threads`)
	require.NoError(t, err)
	_, err = db.Exec(`
		UPDATE pr_development_cases SET capture_hash = ? WHERE id = ?`,
		strings.Repeat("0", 64),
		developmentCase.ID,
	)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 8)
	require.NoError(t, db.Close())

	migrated, err := Open(ctx, path)
	require.Error(t, err)
	assert.Nil(t, migrated)
	assert.Contains(t, err.Error(), "backfill eventing schema v9 threads")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version, cases int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 8, version)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pr_development_cases`).Scan(&cases))
	assert.Equal(t, 1, cases)
	for _, table := range []string{
		"pr_development_threads",
		"pr_development_thread_cases",
	} {
		assert.False(t, schemaObjectExists(t, db, "table", table), table)
	}
}

func TestPRDevelopmentThreadDigestEncodingIsStable(t *testing.T) {
	t.Parallel()

	identity := PRDevelopmentThreadIdentity{
		Provider:       "github",
		ProviderOrigin: "https://github.com",
		PullAuthorID:   "101",
		RepositoryID:   "202",
		PullRequestID:  "303",
		PullNumber:     42,
	}
	identityHash := prDevelopmentProviderThreadIdentityHash(identity)
	assert.Len(t, identityHash, 64)
	previous := emptyPRDevelopmentThreadCasesDigest()
	link := PRDevelopmentThreadCaseLink{
		CaseID:       "pdc_00000000000000000000000000000001",
		Ordinal:      0,
		LinkedAt:     time.Unix(0, 1785945600000000000).UTC(),
		PreviousHash: previous,
	}
	linkHash, err := extendPRDevelopmentThreadCasesDigest(
		"pdt_00000000000000000000000000000002",
		identityHash,
		strings.Repeat("c", 64),
		link,
	)
	require.NoError(t, err)
	assert.Len(t, linkHash, 64)
	assert.NotEqual(t, previous, linkHash)
	assert.False(t, errors.Is(err, ErrPRDevelopmentConflict))
}

func TestPRDevelopmentThreadProviderOriginRejectsTextualIPAliases(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"https://[0:0:0:0:0:0:0:1]",
		"https://[::ffff:127.0.0.1]",
		"https://127.000.000.001",
		"https://0x7f000001",
		"https://0x7f.1",
		"https://127.0.0x0.1",
		"https://ghe.example.test:08443",
	} {
		assert.False(t, validPRDevelopmentProviderOrigin(origin), origin)
	}
	assert.True(t, validPRDevelopmentProviderOrigin("https://[::1]"))
	assert.True(t, validPRDevelopmentProviderOrigin("https://127.0.0.1"))
}

func captureAdditionalPRDevelopmentThreadCase(
	t *testing.T,
	store *Store,
	clock *time.Time,
	base PRDevelopmentCaptureInput,
	deliveryID, reviewID string,
) PRDevelopmentCase {
	t.Helper()
	*clock = clock.Add(time.Minute)
	input := base
	input.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		deliveryID,
		base.WorkflowRef,
		base.WorkflowRevision,
	)
	input.ReviewID = reviewID
	input.TriggerReviewNodeID = "PRR_thread_" + reviewID
	input.ReviewURL = input.PullURL + "#pullrequestreview-" + reviewID
	input.Feedback = "Review feedback " + reviewID
	input.ReviewSubmittedAt = input.ReviewSubmittedAt.Add(time.Minute)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		context.Background(),
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	return developmentCase
}
