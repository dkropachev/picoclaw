//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
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

func TestStorePRDevelopmentConversationAppendGetAndImmutableOrdering(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, input := newPRDevelopmentStoreFixture(t, ":memory:")
	first, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	assertPRDevelopmentConversationState(
		t,
		store,
		first.ID,
		0,
		0,
		emptyPRDevelopmentTranscriptDigest(),
	)

	empty, err := store.GetPRDevelopmentConversation(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, empty.CaseID)
	assert.Zero(t, empty.Version)
	assert.Empty(t, empty.Messages)
	assert.NotNil(t, empty.Messages)

	*clock = (*clock).Add(time.Minute)
	secondInput := input
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		store,
		"delivery-development-conversation-second",
		input.WorkflowRef,
		input.WorkflowRevision,
	)
	second, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(secondInput),
	)
	require.NoError(t, err)
	require.True(t, created)

	*clock = (*clock).Add(time.Minute)
	userConversation, err := store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          first.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "  Help me understand this feedback.  ",
		},
	)
	require.NoError(t, err)
	require.Len(t, userConversation.Messages, 1)
	assert.Equal(t, int64(1), userConversation.Version)
	assert.Equal(t, 0, userConversation.Messages[0].Ordinal)
	assert.Equal(t, PRDevelopmentMessageUser, userConversation.Messages[0].Role)
	assert.Equal(t, "Help me understand this feedback.", userConversation.Messages[0].Content)
	assert.Equal(t, *clock, userConversation.Messages[0].CreatedAt)
	assert.True(
		t,
		validPrefixedHexID(
			userConversation.Messages[0].ID,
			prDevelopmentMessageIDPrefix,
		),
	)

	*clock = (*clock).Add(time.Minute)
	want, err := store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          first.ID,
			ExpectedVersion: 1,
			Role:            PRDevelopmentMessageAssistant,
			Content:         "The reviewer is asking for a bounded retry.",
		},
	)
	require.NoError(t, err)
	require.Len(t, want.Messages, 2)
	assert.Equal(t, int64(2), want.Version)
	assert.Equal(t, 1, want.Messages[1].Ordinal)
	assert.Equal(t, PRDevelopmentMessageAssistant, want.Messages[1].Role)

	loaded, err := store.GetPRDevelopmentConversation(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, want, loaded)
	assertPRDevelopmentConversationState(
		t,
		store,
		first.ID,
		want.Version,
		int64(prDevelopmentTranscriptBytes(want.Messages)),
		prDevelopmentTranscriptDigestForTest(t, want.Messages),
	)

	unchanged, err := store.GetPRDevelopmentCase(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, first, unchanged)
	page, err := store.ListPRDevelopmentCases(ctx, PRDevelopmentCaseFilter{})
	require.NoError(t, err)
	require.Len(t, page.Cases, 2)
	assert.Equal(t, []string{second.ID, first.ID}, []string{
		page.Cases[0].ID,
		page.Cases[1].ID,
	})
}

func TestStorePRDevelopmentConversationOptimisticConflictIsAtomic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)

	committed, err := store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          developmentCase.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "first",
		},
	)
	require.NoError(t, err)

	for _, staleVersion := range []int64{0, 2} {
		_, err = store.AppendPRDevelopmentMessage(
			ctx,
			PRDevelopmentMessageAppend{
				CaseID:          developmentCase.ID,
				ExpectedVersion: staleVersion,
				Role:            PRDevelopmentMessageAssistant,
				Content:         "must not commit",
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentConversationConflict)
	}

	loaded, err := store.GetPRDevelopmentConversation(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, committed, loaded)
}

func TestStorePRDevelopmentCaptureConversationStateFailureIsAtomic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	_, err := store.db.Exec(`
		CREATE TRIGGER fail_pr_development_conversation_state
		BEFORE INSERT ON pr_development_conversations
		BEGIN
			SELECT RAISE(ABORT, 'conversation state unavailable');
		END`)
	require.NoError(t, err)

	_, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.Error(t, err)
	assert.False(t, created)

	var cases, conversations int
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_cases`,
	).Scan(&cases))
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_conversations`,
	).Scan(&conversations))
	assert.Zero(t, cases)
	assert.Zero(t, conversations)
}

func TestStorePRDevelopmentConversationValidatesInputsAndMissingCases(t *testing.T) {
	ctx := context.Background()
	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)

	valid := PRDevelopmentMessageAppend{
		CaseID:          developmentCase.ID,
		ExpectedVersion: 0,
		Role:            PRDevelopmentMessageUser,
		Content:         "valid",
	}
	tests := []struct {
		name   string
		mutate func(*PRDevelopmentMessageAppend)
	}{
		{name: "case ID", mutate: func(input *PRDevelopmentMessageAppend) {
			input.CaseID = "pdc_invalid"
		}},
		{name: "negative version", mutate: func(input *PRDevelopmentMessageAppend) {
			input.ExpectedVersion = -1
		}},
		{name: "role", mutate: func(input *PRDevelopmentMessageAppend) {
			input.Role = "tool"
		}},
		{name: "empty content", mutate: func(input *PRDevelopmentMessageAppend) {
			input.Content = " \n\t "
		}},
		{name: "invalid UTF-8", mutate: func(input *PRDevelopmentMessageAppend) {
			input.Content = string([]byte{0xff})
		}},
		{name: "NUL content", mutate: func(input *PRDevelopmentMessageAppend) {
			input.Content = "before\x00after"
		}},
		{name: "oversized content", mutate: func(input *PRDevelopmentMessageAppend) {
			input.Content = strings.Repeat("x", MaxPRDevelopmentMessageBytes+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			_, appendErr := store.AppendPRDevelopmentMessage(ctx, candidate)
			assert.ErrorIs(t, appendErr, ErrInvalidPRDevelopment)
		})
	}

	missingID := "pdc_00000000000000000000000000000000"
	_, err = store.GetPRDevelopmentConversation(ctx, missingID)
	assert.ErrorIs(t, err, ErrNotFound)
	missing := valid
	missing.CaseID = missingID
	_, err = store.AppendPRDevelopmentMessage(ctx, missing)
	assert.ErrorIs(t, err, ErrNotFound)

	loaded, err := store.GetPRDevelopmentConversation(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Zero(t, loaded.Version)
	assert.Empty(t, loaded.Messages)
}

func TestStorePRDevelopmentConversationExactLimits(t *testing.T) {
	t.Run("message count", func(t *testing.T) {
		ctx := context.Background()
		store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
		developmentCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(input),
		)
		require.NoError(t, err)
		require.True(t, created)

		var conversation PRDevelopmentConversation
		for version := int64(0); version < MaxPRDevelopmentMessagesPerCase; version++ {
			conversation, err = store.AppendPRDevelopmentMessage(
				ctx,
				PRDevelopmentMessageAppend{
					CaseID:          developmentCase.ID,
					ExpectedVersion: version,
					Role:            PRDevelopmentMessageUser,
					Content:         "x",
				},
			)
			require.NoError(t, err)
		}
		require.Len(t, conversation.Messages, MaxPRDevelopmentMessagesPerCase)
		_, err = store.AppendPRDevelopmentMessage(
			ctx,
			PRDevelopmentMessageAppend{
				CaseID:          developmentCase.ID,
				ExpectedVersion: conversation.Version,
				Role:            PRDevelopmentMessageAssistant,
				Content:         "overflow",
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentConversationCapacity)

		unchanged, err := store.GetPRDevelopmentConversation(ctx, developmentCase.ID)
		require.NoError(t, err)
		assert.Equal(t, conversation, unchanged)
	})

	t.Run("UTF-8 message and transcript bytes", func(t *testing.T) {
		ctx := context.Background()
		store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
		developmentCase, created, err := store.CapturePRDevelopmentCase(
			ctx,
			validPRDevelopmentRequestForTest(input),
		)
		require.NoError(t, err)
		require.True(t, created)

		maximumMessage := strings.Repeat("é", MaxPRDevelopmentMessageBytes/2)
		require.Len(t, []byte(maximumMessage), MaxPRDevelopmentMessageBytes)
		messageCount := MaxPRDevelopmentTranscriptBytes / MaxPRDevelopmentMessageBytes
		var conversation PRDevelopmentConversation
		for version := 0; version < messageCount; version++ {
			conversation, err = store.AppendPRDevelopmentMessage(
				ctx,
				PRDevelopmentMessageAppend{
					CaseID:          developmentCase.ID,
					ExpectedVersion: int64(version),
					Role:            PRDevelopmentMessageAssistant,
					Content:         maximumMessage,
				},
			)
			require.NoError(t, err)
		}
		require.Len(t, conversation.Messages, messageCount)
		assert.Equal(
			t,
			MaxPRDevelopmentTranscriptBytes,
			prDevelopmentTranscriptBytes(conversation.Messages),
		)
		_, err = store.AppendPRDevelopmentMessage(
			ctx,
			PRDevelopmentMessageAppend{
				CaseID:          developmentCase.ID,
				ExpectedVersion: conversation.Version,
				Role:            PRDevelopmentMessageUser,
				Content:         "x",
			},
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentConversationCapacity)

		unchanged, err := store.GetPRDevelopmentConversation(ctx, developmentCase.ID)
		require.NoError(t, err)
		assert.Equal(t, conversation, unchanged)
	})
}

func TestStorePRDevelopmentConversationConcurrentWritersFenceVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conversation-concurrency.db")
	first, clock, input := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := first.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	second, err := Open(ctx, path, WithClock(func() time.Time { return *clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	type appendResult struct {
		conversation PRDevelopmentConversation
		err          error
	}
	start := make(chan struct{})
	results := make(chan appendResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	appendWith := func(store *Store, content string) {
		ready.Done()
		<-start
		conversation, appendErr := store.AppendPRDevelopmentMessage(
			ctx,
			PRDevelopmentMessageAppend{
				CaseID:          developmentCase.ID,
				ExpectedVersion: 0,
				Role:            PRDevelopmentMessageUser,
				Content:         content,
			},
		)
		results <- appendResult{conversation: conversation, err: appendErr}
	}
	go appendWith(first, "first writer")
	go appendWith(second, "second writer")
	ready.Wait()
	close(start)

	succeeded := 0
	conflicted := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			assert.Equal(t, int64(1), result.conversation.Version)
		case errors.Is(result.err, ErrPRDevelopmentConversationConflict):
			conflicted++
		default:
			require.NoError(t, result.err)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, conflicted)
	loaded, err := first.GetPRDevelopmentConversation(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 1)
	assert.Equal(t, int64(1), loaded.Version)
}

func TestStorePRDevelopmentConversationRejectsStoredCorruption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, string)
	}{
		{
			name: "ordinal gap",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages SET ordinal = 2 WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "tail deletion",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`DELETE FROM pr_development_messages WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "message ID",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages SET id = 'invalid' WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "valid content alteration",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages SET content = 'other' WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "role",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(`PRAGMA ignore_check_constraints = ON`)
				require.NoError(t, err)
				_, err = store.db.Exec(
					`UPDATE pr_development_messages SET role = 'tool' WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "invalid UTF-8",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages SET content = CAST(X'80' AS TEXT) WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "NUL content",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages
					 SET content = 'before' || char(0) || 'after'
					 WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "noncanonical whitespace",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages SET content = ' padded ' WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "timestamp shape",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(
					`UPDATE pr_development_messages SET created_at = 'invalid' WHERE id = ?`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "missing conversation state",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(`
					DELETE FROM pr_development_conversations
					WHERE case_id = (
						SELECT case_id FROM pr_development_messages WHERE id = ?
					)`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "conversation version",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_conversations SET version = 2
					WHERE case_id = (
						SELECT case_id FROM pr_development_messages WHERE id = ?
					)`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "conversation content bytes",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_conversations SET content_bytes = 1
					WHERE case_id = (
						SELECT case_id FROM pr_development_messages WHERE id = ?
					)`,
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "conversation digest",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_conversations SET transcript_digest = ?
					WHERE case_id = (
						SELECT case_id FROM pr_development_messages WHERE id = ?
					)`,
					strings.Repeat("0", 64),
					messageID,
				)
				require.NoError(t, err)
			},
		},
		{
			name: "aggregate transcript bytes",
			mutate: func(t *testing.T, store *Store, messageID string) {
				t.Helper()
				var createdAt int64
				require.NoError(t, store.db.QueryRow(
					`SELECT created_at FROM pr_development_messages WHERE id = ?`,
					messageID,
				).Scan(&createdAt))
				_, err := store.db.Exec(`DELETE FROM pr_development_messages`)
				require.NoError(t, err)
				maximumMessage := strings.Repeat("x", MaxPRDevelopmentMessageBytes)
				for ordinal := 0; ordinal <=
					MaxPRDevelopmentTranscriptBytes/MaxPRDevelopmentMessageBytes; ordinal++ {
					_, err = store.db.Exec(`
						INSERT INTO pr_development_messages (
							id, case_id, ordinal, role, content, created_at
						) VALUES (?, (SELECT id FROM pr_development_cases LIMIT 1), ?, ?, ?, ?)`,
						fmt.Sprintf("pdm_%032x", ordinal+1),
						ordinal,
						PRDevelopmentMessageAssistant,
						maximumMessage,
						createdAt,
					)
					require.NoError(t, err)
				}
			},
		},
	}
	for _, test := range tests {
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
			conversation, err := store.AppendPRDevelopmentMessage(
				ctx,
				PRDevelopmentMessageAppend{
					CaseID:          developmentCase.ID,
					ExpectedVersion: 0,
					Role:            PRDevelopmentMessageUser,
					Content:         "valid",
				},
			)
			require.NoError(t, err)
			require.Len(t, conversation.Messages, 1)
			test.mutate(t, store, conversation.Messages[0].ID)
			var countBefore int
			require.NoError(t, store.db.QueryRow(`
				SELECT COUNT(*) FROM pr_development_messages WHERE case_id = ?`,
				developmentCase.ID,
			).Scan(&countBefore))

			_, err = store.GetPRDevelopmentConversation(ctx, developmentCase.ID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "stored pull request development")
			_, err = store.AppendPRDevelopmentMessage(
				ctx,
				PRDevelopmentMessageAppend{
					CaseID:          developmentCase.ID,
					ExpectedVersion: 1,
					Role:            PRDevelopmentMessageAssistant,
					Content:         "must not append",
				},
			)
			require.Error(t, err)

			var count int
			require.NoError(t, store.db.QueryRow(`
				SELECT COUNT(*) FROM pr_development_messages WHERE case_id = ?`,
				developmentCase.ID,
			).Scan(&count))
			assert.Equal(t, countBefore, count)
		})
	}
}

func TestStorePRDevelopmentConversationHonorsContextAndClosedStore(t *testing.T) {
	t.Parallel()

	store, _, input := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		context.Background(),
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.GetPRDevelopmentConversation(canceled, developmentCase.ID)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = store.AppendPRDevelopmentMessage(
		canceled,
		PRDevelopmentMessageAppend{
			CaseID:          developmentCase.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "canceled",
		},
	)
	assert.ErrorIs(t, err, context.Canceled)

	require.NoError(t, store.Close())
	_, err = store.GetPRDevelopmentConversation(context.Background(), developmentCase.ID)
	assert.ErrorIs(t, err, ErrClosed)
	_, err = store.AppendPRDevelopmentMessage(
		context.Background(),
		PRDevelopmentMessageAppend{
			CaseID:          developmentCase.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "closed",
		},
	)
	assert.ErrorIs(t, err, ErrClosed)
}

func TestStoreMigratesV6ToPRDevelopmentConversationSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v6-pr-development-chat.db")
	db := openSchemaTestDB(t, path)
	installEventingSchemaThroughV6(t, db)
	setSchemaTestVersion(t, db, 6)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	assert.True(
		t,
		schemaObjectExists(t, store.db, "table", "pr_development_conversations"),
	)
	assert.True(
		t,
		schemaObjectExists(t, store.db, "table", "pr_development_messages"),
	)
}

func TestStoreMigratesV6BackfillsExistingPRDevelopmentConversationState(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-v6-existing-development-case.db")
	legacyStore, _, input := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := legacyStore.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(input),
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, legacyStore.Close())

	db := openSchemaTestDB(t, path)
	_, err = db.Exec(`DROP TABLE pr_development_thread_cases`)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE pr_development_threads`)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE pr_development_messages`)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE pr_development_conversations`)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 6)
	require.NoError(t, db.Close())

	store, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	conversation, err := store.GetPRDevelopmentConversation(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, developmentCase.ID, conversation.CaseID)
	assert.Zero(t, conversation.Version)
	assert.Empty(t, conversation.Messages)
	assertPRDevelopmentConversationState(
		t,
		store,
		developmentCase.ID,
		0,
		0,
		emptyPRDevelopmentTranscriptDigest(),
	)

	unchanged, err := store.GetPRDevelopmentCase(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, developmentCase, unchanged)
}

func TestStorePRDevelopmentConversationMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	malformed := strings.Replace(
		schemaV7PRDevelopmentMessagesTable,
		"'user', 'assistant'",
		"'user', 'assistant', 'tool'",
		1,
	)
	assertPRDevelopmentMigrationValidationRollsBack(
		t,
		6,
		installEventingSchemaThroughV6,
		malformed,
		"validate eventing schema v7",
		"table",
		"pr_development_conversations",
	)
}

func TestStoreRejectsInvalidCurrentPRDevelopmentConversationSchema(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		installV7  func(*testing.T, *sql.DB)
		wantObject string
	}{
		{
			name:       "missing conversation table",
			installV7:  func(*testing.T, *sql.DB) {},
			wantObject: "pr_development_conversations",
		},
		{
			name: "missing messages table",
			installV7: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(schemaV7PRDevelopmentConversationsTable)
				require.NoError(t, err)
			},
			wantObject: "pr_development_messages",
		},
		{
			name: "malformed conversation table",
			installV7: func(t *testing.T, db *sql.DB) {
				t.Helper()
				malformed := strings.Replace(
					schemaV7PRDevelopmentConversationsTable,
					"version <= 256",
					"version <= 257",
					1,
				)
				_, err := db.Exec(malformed)
				require.NoError(t, err)
			},
			wantObject: "pr_development_conversations",
		},
		{
			name: "malformed messages table",
			installV7: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(schemaV7PRDevelopmentConversationsTable)
				require.NoError(t, err)
				malformed := strings.Replace(
					schemaV7PRDevelopmentMessagesTable,
					"UNIQUE(case_id, ordinal)",
					"UNIQUE(case_id, ordinal, role)",
					1,
				)
				_, err = db.Exec(malformed)
				require.NoError(t, err)
			},
			wantObject: "pr_development_messages",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "invalid-current-v7.db")
			db := openSchemaTestDB(t, path)
			installEventingSchemaThroughV6(t, db)
			test.installV7(t, db)
			setSchemaTestVersion(t, db, schemaVersion)
			require.NoError(t, db.Close())

			store, err := Open(context.Background(), path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.ErrorIs(t, err, ErrSchemaInvalid)
			assert.Contains(t, err.Error(), "validate eventing schema v7")
			var validationErr *schemaValidationError
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, test.wantObject, validationErr.object)
		})
	}
}

func TestPRDevelopmentTranscriptDigestEncodingIsStable(t *testing.T) {
	t.Parallel()

	const emptyDigest = "dd9275465644a747e52a5fe789f5e0a4" +
		"8c39308a0fb0f4b3990f3d2cd3680a9f"
	assert.Equal(t, emptyDigest, emptyPRDevelopmentTranscriptDigest())
	digest, err := extendPRDevelopmentTranscriptDigest(
		emptyDigest,
		PRDevelopmentMessage{
			ID:        "pdm_00000000000000000000000000000001",
			CaseID:    "pdc_00000000000000000000000000000002",
			Ordinal:   0,
			Role:      PRDevelopmentMessageUser,
			Content:   "hello",
			CreatedAt: time.Unix(0, 1785945600000000000).UTC(),
		},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		"9382855062dab424058bccb9a7c312ba313fdc3a2ceb526f20402c59f38c8b89",
		digest,
	)
}

func assertPRDevelopmentConversationState(
	t *testing.T,
	store *Store,
	caseID string,
	wantVersion int64,
	wantContentBytes int64,
	wantDigest string,
) {
	t.Helper()
	var (
		version      int64
		contentBytes int64
		digest       string
	)
	require.NoError(t, store.db.QueryRow(`
		SELECT version, content_bytes, transcript_digest
		FROM pr_development_conversations
		WHERE case_id = ?`,
		caseID,
	).Scan(&version, &contentBytes, &digest))
	assert.Equal(t, wantVersion, version)
	assert.Equal(t, wantContentBytes, contentBytes)
	assert.Equal(t, wantDigest, digest)
}

func prDevelopmentTranscriptDigestForTest(
	t *testing.T,
	messages []PRDevelopmentMessage,
) string {
	t.Helper()
	digest := emptyPRDevelopmentTranscriptDigest()
	for _, message := range messages {
		var err error
		digest, err = extendPRDevelopmentTranscriptDigest(digest, message)
		require.NoError(t, err)
	}
	return digest
}

func installEventingSchemaThroughV6(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
}
