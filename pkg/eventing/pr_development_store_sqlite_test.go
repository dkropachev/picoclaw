//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePRDevelopmentCaptureLookupAndExactIdempotency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, input := newPRDevelopmentStoreFixture(t, ":memory:")

	before, found, err := store.LookupPRDevelopmentCapture(
		ctx,
		input.PRDevelopmentCaptureIdentity,
	)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, PRDevelopmentCase{}, before)

	developmentCase, created, err := store.CapturePRDevelopmentCase(ctx, input)
	require.NoError(t, err)
	assert.True(t, created)
	assert.True(t, validPrefixedHexID(developmentCase.ID, prDevelopmentCaseIDPrefix))
	assert.Equal(t, input, developmentCase.PRDevelopmentCaptureInput)
	assert.Equal(t, *clock, developmentCase.CreatedAt)
	assert.Equal(t, *clock, developmentCase.UpdatedAt)
	assert.Equal(t, "  Preserve provider feedback exactly.\n", developmentCase.Feedback)

	*clock = clock.Add(time.Hour)
	retry, created, err := store.CapturePRDevelopmentCase(ctx, input)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, developmentCase, retry)

	lookup, found, err := store.LookupPRDevelopmentCapture(
		ctx,
		input.PRDevelopmentCaptureIdentity,
	)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, developmentCase, lookup)

	loaded, err := store.GetPRDevelopmentCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, developmentCase, loaded)

	var count int
	require.NoError(t, store.db.QueryRow(`SELECT count(*) FROM pr_development_cases`).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestStorePRDevelopmentCaptureExplicitReplayCreatesDistinctCase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, input := newPRDevelopmentStoreFixture(t, ":memory:")
	first, created, err := store.CapturePRDevelopmentCase(ctx, input)
	require.NoError(t, err)
	require.True(t, created)

	*clock = clock.Add(time.Minute)
	replay := input
	replay.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"delivery-development-replay",
		input.WorkflowRef,
		input.WorkflowRevision,
	)
	second, created, err := store.CapturePRDevelopmentCase(ctx, replay)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, first.ReviewID, second.ReviewID)
	assert.NotEqual(t, first.DispatchID, second.DispatchID)
	assert.Equal(t, *clock, second.CreatedAt)

	firstLookup, found, err := store.LookupPRDevelopmentCapture(
		ctx,
		input.PRDevelopmentCaptureIdentity,
	)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, first.ID, firstLookup.ID)
	secondLookup, found, err := store.LookupPRDevelopmentCapture(
		ctx,
		replay.PRDevelopmentCaptureIdentity,
	)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, second.ID, secondLookup.ID)
}

func TestStorePRDevelopmentCaptureRejectsChangedExactRetry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*PRDevelopmentCaptureInput)
	}{
		{
			name: "provider content",
			mutate: func(changed *PRDevelopmentCaptureInput) {
				changed.Feedback = "different provider feedback"
			},
		},
		{
			name: "review identity",
			mutate: func(changed *PRDevelopmentCaptureInput) {
				changed.ReviewID = "502"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
			_, created, err := store.CapturePRDevelopmentCase(ctx, input)
			require.NoError(t, err)
			require.True(t, created)

			changed := input
			test.mutate(&changed)
			_, created, captureErr := store.CapturePRDevelopmentCase(ctx, changed)
			assert.False(t, created)
			assert.ErrorIs(t, captureErr, ErrPRDevelopmentConflict)
		})
	}
}

func TestStorePRDevelopmentCaptureVerifiesDispatchProvenance(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*PRDevelopmentCaptureIdentity)
	}{
		{
			name: "event",
			mutate: func(identity *PRDevelopmentCaptureIdentity) {
				identity.EventID = "ev_00000000000000000000000000000000"
			},
		},
		{
			name: "run",
			mutate: func(identity *PRDevelopmentCaptureIdentity) {
				identity.RunID = "wr_00000000000000000000000000000000"
			},
		},
		{
			name: "workflow reference",
			mutate: func(identity *PRDevelopmentCaptureIdentity) {
				identity.WorkflowRef = "workflows/other.yml"
			},
		},
		{
			name: "workflow revision",
			mutate: func(identity *PRDevelopmentCaptureIdentity) {
				identity.WorkflowRevision = "revision-other"
			},
		},
		{
			name: "connector",
			mutate: func(identity *PRDevelopmentCaptureIdentity) {
				identity.Connector = "github-other"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
			identity := input.PRDevelopmentCaptureIdentity
			test.mutate(&identity)

			_, found, lookupErr := store.LookupPRDevelopmentCapture(ctx, identity)
			assert.False(t, found)
			assert.ErrorIs(t, lookupErr, ErrPRDevelopmentConflict)

			input.PRDevelopmentCaptureIdentity = identity
			_, created, captureErr := store.CapturePRDevelopmentCase(ctx, input)
			assert.False(t, created)
			assert.ErrorIs(t, captureErr, ErrPRDevelopmentConflict)
		})
	}

	t.Run("missing dispatch", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
		input.DispatchID = "dsp_00000000000000000000000000000000"
		_, found, err := store.LookupPRDevelopmentCapture(
			ctx,
			input.PRDevelopmentCaptureIdentity,
		)
		assert.False(t, found)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestNormalizePRDevelopmentCaptureRequiresCanonicalVerifiedIdentity(t *testing.T) {
	t.Parallel()

	valid := validPRDevelopmentInputForTest()
	maximumFeedback := strings.Repeat("x", maxPRDevelopmentFeedbackBytes-2) + "\x00y"
	maximum := valid
	maximum.Feedback = maximumFeedback
	normalized, err := normalizePRDevelopmentCapture(maximum)
	require.NoError(t, err)
	assert.Equal(t, maximumFeedback, normalized.Feedback)

	dismissed := valid
	dismissed.CurrentReviewState = PRDevelopmentReviewDismissed
	normalized, err = normalizePRDevelopmentCapture(dismissed)
	require.NoError(t, err)
	assert.Equal(t, dismissed, normalized)

	for _, test := range []struct {
		name   string
		mutate func(*PRDevelopmentCaptureInput)
	}{
		{name: "workflow revision missing", mutate: func(input *PRDevelopmentCaptureInput) {
			input.WorkflowRevision = ""
		}},
		{name: "repository malformed", mutate: func(input *PRDevelopmentCaptureInput) {
			input.Repository = "missing-owner"
		}},
		{name: "base repository mismatch", mutate: func(input *PRDevelopmentCaptureInput) {
			input.BaseRepository = "other/project"
		}},
		{name: "pull number zero", mutate: func(input *PRDevelopmentCaptureInput) {
			input.PullNumber = 0
		}},
		{name: "pull URL not HTTPS", mutate: func(input *PRDevelopmentCaptureInput) {
			input.PullURL = "http://github.com/acme/project/pull/42"
		}},
		{name: "pull URL has credentials", mutate: func(input *PRDevelopmentCaptureInput) {
			input.PullURL = "https://user@github.com/acme/project/pull/42"
		}},
		{name: "target is not pull author", mutate: func(input *PRDevelopmentCaptureInput) {
			input.TargetUser = "other-user"
		}},
		{name: "pull state invalid", mutate: func(input *PRDevelopmentCaptureInput) {
			input.PullState = "merged"
		}},
		{name: "merged pull remains open", mutate: func(input *PRDevelopmentCaptureInput) {
			input.PullMerged = true
		}},
		{name: "base ref malformed", mutate: func(input *PRDevelopmentCaptureInput) {
			input.BaseRef = "refs//main"
		}},
		{name: "head SHA uppercase", mutate: func(input *PRDevelopmentCaptureInput) {
			input.HeadSHA = strings.Repeat("A", 40)
		}},
		{name: "review ID zero", mutate: func(input *PRDevelopmentCaptureInput) {
			input.ReviewID = "0"
		}},
		{name: "review ID leading zero", mutate: func(input *PRDevelopmentCaptureInput) {
			input.ReviewID = "0501"
		}},
		{name: "review ID over int64", mutate: func(input *PRDevelopmentCaptureInput) {
			input.ReviewID = "9223372036854775808"
		}},
		{name: "trigger node invalid", mutate: func(input *PRDevelopmentCaptureInput) {
			input.TriggerReviewNodeID = "node!"
		}},
		{name: "self review", mutate: func(input *PRDevelopmentCaptureInput) {
			input.ReviewAuthor = "review-user"
		}},
		{name: "submitted review dismissed", mutate: func(input *PRDevelopmentCaptureInput) {
			input.SubmittedReviewState = PRDevelopmentReviewDismissed
		}},
		{name: "current review changed to another submitted state", mutate: func(input *PRDevelopmentCaptureInput) {
			input.CurrentReviewState = PRDevelopmentReviewApproved
		}},
		{name: "submitted time not UTC", mutate: func(input *PRDevelopmentCaptureInput) {
			input.ReviewSubmittedAt = time.Date(
				2026,
				time.August,
				5,
				8,
				34,
				56,
				0,
				time.FixedZone("EDT", -4*60*60),
			)
		}},
		{name: "review URL not HTTPS", mutate: func(input *PRDevelopmentCaptureInput) {
			input.ReviewURL = "http://github.com/acme/project/pull/42#pullrequestreview-501"
		}},
		{name: "feedback invalid UTF-8", mutate: func(input *PRDevelopmentCaptureInput) {
			input.Feedback = string([]byte{0xff})
		}},
		{name: "feedback oversized", mutate: func(input *PRDevelopmentCaptureInput) {
			input.Feedback = strings.Repeat("x", maxPRDevelopmentFeedbackBytes+1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := valid
			test.mutate(&input)
			_, normalizeErr := normalizePRDevelopmentCapture(input)
			assert.ErrorIs(t, normalizeErr, ErrInvalidPRDevelopment)
		})
	}
}

func TestStoreMigratesV5ToPRDevelopmentSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v5-pr-development.db")
	db := openSchemaTestDB(t, path)
	installEventingSchemaThroughV5(t, db)
	setSchemaTestVersion(t, db, 5)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()

	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "pr_development_cases"},
		{kind: "index", name: "pr_development_cases_list"},
		{kind: "index", name: "pr_development_cases_repository"},
	} {
		assert.True(t, schemaObjectExists(t, store.db, object.kind, object.name))
	}
}

func TestStorePRDevelopmentMigrationValidationFailureRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v6-pr-development-rollback.db")
	db := openSchemaTestDB(t, path)
	installEventingSchemaThroughV5(t, db)
	malformed := strings.Replace(
		schemaV6PRDevelopmentCasesTable,
		"'approved', 'changes_requested', 'commented'",
		"'approved', 'changes_requested', 'commented', 'pending'",
		1,
	)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 5)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v6")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 5, version)
	assert.False(t, schemaObjectExists(t, db, "index", "pr_development_cases_list"))
}

func TestStoreRejectsCurrentPRDevelopmentSchemaMissingObjects(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		setup      func(*testing.T, *sql.DB)
		wantObject string
	}{
		{
			name: "table",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				installEventingSchemaThroughV5(t, db)
				setSchemaTestVersion(t, db, schemaVersion)
			},
			wantObject: "pr_development_cases",
		},
		{
			name: "index",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				installEventingSchemaThroughV5(t, db)
				_, err := db.Exec(schemaV6)
				require.NoError(t, err)
				_, err = db.Exec(`DROP INDEX pr_development_cases_repository`)
				require.NoError(t, err)
				setSchemaTestVersion(t, db, schemaVersion)
			},
			wantObject: "pr_development_cases_repository",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "invalid-v6-pr-development.db")
			db := openSchemaTestDB(t, path)
			test.setup(t, db)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			assert.Contains(t, err.Error(), "validate eventing schema v6")
			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, test.wantObject, validationErr.object)
		})
	}
}

func TestStorePruneRetainsEventsReferencedByPRDevelopmentCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, input := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(ctx, input)
	require.NoError(t, err)
	require.True(t, created)

	unreferenced, err := store.Insert(ctx, Envelope{
		Source:    "github",
		Connector: input.Connector,
		Type:      "pull_request_review.submitted",
		DedupeKey: "delivery-development-unreferenced",
		Payload:   json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	_, err = store.db.Exec(`
		UPDATE event_inbox
		SET routing_status = ?, received_at = ?
		WHERE id IN (?, ?)`,
		RoutingSucceeded,
		toDBTime(clock.Add(-time.Hour)),
		input.EventID,
		unreferenced.Event.Envelope.ID,
	)
	require.NoError(t, err)
	_, err = store.db.Exec(`
		UPDATE event_dispatches
		SET status = ?, finished_at = ?
		WHERE id = ?`,
		DispatchSucceeded,
		toDBTime(*clock),
		input.DispatchID,
	)
	require.NoError(t, err)

	pruned, err := store.Prune(ctx, clock.Add(time.Minute), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pruned)
	_, err = store.Get(ctx, input.EventID)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	_, err = store.Get(ctx, unreferenced.Event.Envelope.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func newPRDevelopmentStoreFixture(
	t *testing.T,
	databasePath string,
) (*Store, *time.Time, PRDevelopmentCaptureInput) {
	t.Helper()

	now := time.Date(2026, time.August, 5, 16, 0, 0, 0, time.UTC)
	store, err := Open(
		context.Background(),
		databasePath,
		WithClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	identity := addPRDevelopmentDispatch(
		t,
		store,
		"delivery-development-capture",
		"workflows/own-pr-feedback.yml",
		"revision-2026-08-05",
	)
	input := validPRDevelopmentInputForTest()
	input.PRDevelopmentCaptureIdentity = identity
	return store, &now, input
}

func addPRDevelopmentDispatch(
	t *testing.T,
	store *Store,
	deliveryID, workflowRef, workflowRevision string,
) PRDevelopmentCaptureIdentity {
	t.Helper()

	ctx := context.Background()
	inserted, err := store.Insert(ctx, Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request_review.submitted",
		DedupeKey: deliveryID,
		Payload:   json.RawMessage(`{}`),
		Attributes: map[string]string{
			"body_authenticated": "true",
			"target_reason":      "review_feedback",
		},
	})
	require.NoError(t, err)
	claimed, err := store.ClaimRouting(ctx, "development-router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, inserted.Event.Envelope.ID, claimed[0].Envelope.ID)
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		workflowRef,
		workflowRevision,
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.AckRouting(
		ctx,
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
	))
	return PRDevelopmentCaptureIdentity{
		EventID:          inserted.Event.Envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            dispatch.RunID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        inserted.Event.Envelope.Connector,
	}
}

func validPRDevelopmentInputForTest() PRDevelopmentCaptureInput {
	return PRDevelopmentCaptureInput{
		PRDevelopmentCaptureIdentity: PRDevelopmentCaptureIdentity{
			EventID:          "ev_00000000000000000000000000000001",
			DispatchID:       "dsp_00000000000000000000000000000001",
			RunID:            "wr_00000000000000000000000000000001",
			WorkflowRef:      "workflows/own-pr-feedback.yml",
			WorkflowRevision: "revision-2026-08-05",
			Connector:        "github-primary",
		},
		Repository:           "acme/project",
		PullNumber:           42,
		PullURL:              "https://github.com/acme/project/pull/42",
		PullAuthor:           "Review-User",
		TargetUser:           "review-user",
		PullState:            PRDevelopmentPullOpen,
		BaseRepository:       "Acme/Project",
		BaseRef:              "main",
		BaseSHA:              strings.Repeat("1", 40),
		HeadRepository:       "review-user/project-fork",
		HeadRef:              "repair/retries",
		HeadSHA:              strings.Repeat("2", 40),
		ReviewID:             "501",
		TriggerReviewNodeID:  "PRR_kwDOReview501",
		ReviewAuthor:         "maintainer-1",
		SubmittedReviewState: PRDevelopmentReviewChangesRequested,
		CurrentReviewState:   PRDevelopmentReviewChangesRequested,
		ReviewCommitSHA:      strings.Repeat("a", 40),
		ReviewSubmittedAt: time.Date(
			2026,
			time.August,
			5,
			12,
			34,
			56,
			123456789,
			time.UTC,
		),
		ReviewURL: "https://github.com/acme/project/pull/42#pullrequestreview-501",
		Feedback:  "  Preserve provider feedback exactly.\n",
	}
}

func installEventingSchemaThroughV5(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, schema := range []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
}
