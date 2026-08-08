//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"unicode/utf8"
)

var _ PRDevelopmentConversationStore = (*Store)(nil)

const (
	prDevelopmentConversationStateColumns = `
		case_id, version, content_bytes, transcript_digest`
	prDevelopmentMessageColumns = `
		id, case_id, ordinal, role, content, created_at`
	prDevelopmentTranscriptDigestDomain = "picoclaw-pr-development-transcript-v1"
)

type loadedPRDevelopmentConversation struct {
	Conversation     PRDevelopmentConversation
	ContentBytes     int64
	TranscriptDigest string
}

type storedPRDevelopmentConversationState struct {
	CaseID           string
	Version          int64
	ContentBytes     int64
	TranscriptDigest string
}

// GetPRDevelopmentConversation returns one exact, integrity-checked transcript.
// Its durable high-water state must match the contiguous message count, byte
// total, and rolling digest; reading never updates the immutable development
// case or its list position.
func (s *Store) GetPRDevelopmentConversation(
	ctx context.Context,
	caseID string,
) (PRDevelopmentConversation, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentConversation{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentConversation{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopment,
		)
	}

	var conversation PRDevelopmentConversation
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := loadPRDevelopmentConversation(ctx, queryer, caseID)
			conversation = loaded.Conversation
			return loadErr
		},
	)
	if err != nil {
		return PRDevelopmentConversation{}, fmt.Errorf(
			"get pull request development conversation: %w",
			s.dbError(err),
		)
	}
	return conversation, nil
}

// withPRDevelopmentConversationReadSnapshot holds one read transaction across
// case validation and transcript loading. WAL readers observe one atomic
// snapshot without reserving the single writer.
func (s *Store) withPRDevelopmentConversationReadSnapshot(
	ctx context.Context,
	operation func(rowsQueryer) error,
) (err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = operation(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendPRDevelopmentMessage atomically validates the complete existing
// transcript, checks its persisted version, and appends exactly one message
// while advancing its high-water state. The immutable development-case row is
// deliberately never updated.
func (s *Store) AppendPRDevelopmentMessage(
	ctx context.Context,
	input PRDevelopmentMessageAppend,
) (PRDevelopmentConversation, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentConversation{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	if !validPrefixedHexID(input.CaseID, prDevelopmentCaseIDPrefix) ||
		input.ExpectedVersion < 0 {
		return PRDevelopmentConversation{}, fmt.Errorf(
			"%w: valid case ID and nonnegative expected version are required",
			ErrInvalidPRDevelopment,
		)
	}
	if !validPRDevelopmentMessageRole(input.Role) {
		return PRDevelopmentConversation{}, fmt.Errorf(
			"%w: unknown development message role %q",
			ErrInvalidPRDevelopment,
			input.Role,
		)
	}
	content, err := normalizePRDevelopmentMessageContent(input.Content)
	if err != nil {
		return PRDevelopmentConversation{}, err
	}

	var conversation PRDevelopmentConversation
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := loadPRDevelopmentConversation(ctx, conn, input.CaseID)
		if loadErr != nil {
			return loadErr
		}
		if current.Conversation.Version != input.ExpectedVersion {
			return fmt.Errorf(
				"%w: expected version %d, current version %d",
				ErrPRDevelopmentConversationConflict,
				input.ExpectedVersion,
				current.Conversation.Version,
			)
		}
		if len(current.Conversation.Messages) >= MaxPRDevelopmentMessagesPerCase {
			return fmt.Errorf(
				"%w: transcript cannot exceed %d messages",
				ErrPRDevelopmentConversationCapacity,
				MaxPRDevelopmentMessagesPerCase,
			)
		}
		if int64(len(content)) >
			int64(MaxPRDevelopmentTranscriptBytes)-current.ContentBytes {
			return fmt.Errorf(
				"%w: transcript cannot exceed %d bytes",
				ErrPRDevelopmentConversationCapacity,
				MaxPRDevelopmentTranscriptBytes,
			)
		}

		messageID, idErr := newPrefixedID(prDevelopmentMessageIDPrefix)
		if idErr != nil {
			return idErr
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		message := PRDevelopmentMessage{
			ID:        messageID,
			CaseID:    input.CaseID,
			Ordinal:   len(current.Conversation.Messages),
			Role:      input.Role,
			Content:   content,
			CreatedAt: now,
		}
		nextDigest, digestErr := extendPRDevelopmentTranscriptDigest(
			current.TranscriptDigest,
			message,
		)
		if digestErr != nil {
			return digestErr
		}
		nextVersion := current.Conversation.Version + 1
		nextContentBytes := current.ContentBytes + int64(len(content))
		if _, execErr := conn.ExecContext(ctx, `
			INSERT INTO pr_development_messages (
				id, case_id, ordinal, role, content, created_at
			) VALUES (?, ?, ?, ?, ?, ?)`,
			messageID,
			input.CaseID,
			current.Conversation.Version,
			input.Role,
			content,
			toDBTime(now),
		); execErr != nil {
			return execErr
		}
		result, execErr := conn.ExecContext(ctx, `
			UPDATE pr_development_conversations
			SET version = ?, content_bytes = ?, transcript_digest = ?
			WHERE case_id = ? AND version = ? AND content_bytes = ?
				AND transcript_digest = ?`,
			nextVersion,
			nextContentBytes,
			nextDigest,
			input.CaseID,
			current.Conversation.Version,
			current.ContentBytes,
			current.TranscriptDigest,
		)
		if execErr != nil {
			return execErr
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if affected != 1 {
			return fmt.Errorf(
				"stored pull request development conversation state changed unexpectedly",
			)
		}
		messages := make(
			[]PRDevelopmentMessage,
			len(current.Conversation.Messages)+1,
		)
		copy(messages, current.Conversation.Messages)
		messages[len(messages)-1] = message
		conversation = PRDevelopmentConversation{
			CaseID:   input.CaseID,
			Version:  nextVersion,
			Messages: messages,
		}
		return nil
	})
	if err != nil {
		return PRDevelopmentConversation{}, fmt.Errorf(
			"append pull request development message: %w",
			s.dbError(err),
		)
	}
	return conversation, nil
}

func loadPRDevelopmentConversation(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
) (loadedPRDevelopmentConversation, error) {
	if _, err := getPRDevelopmentCaseRecord(ctx, queryer, caseID); err != nil {
		return loadedPRDevelopmentConversation{}, err
	}
	state, err := getPRDevelopmentConversationState(ctx, queryer, caseID)
	if err != nil {
		return loadedPRDevelopmentConversation{}, err
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+prDevelopmentMessageColumns+`
		FROM pr_development_messages
		WHERE case_id = ?
		ORDER BY ordinal`,
		caseID,
	)
	if err != nil {
		return loadedPRDevelopmentConversation{}, err
	}
	defer func() { _ = rows.Close() }()

	messages := make([]PRDevelopmentMessage, 0)
	contentBytes := int64(0)
	transcriptDigest := emptyPRDevelopmentTranscriptDigest()
	for rows.Next() {
		message, scanErr := scanPRDevelopmentMessage(rows)
		if scanErr != nil {
			return loadedPRDevelopmentConversation{}, scanErr
		}
		if message.CaseID != caseID {
			return loadedPRDevelopmentConversation{}, fmt.Errorf(
				"stored pull request development message case ID is invalid",
			)
		}
		if message.Ordinal != len(messages) {
			return loadedPRDevelopmentConversation{}, fmt.Errorf(
				"stored pull request development message ordinal is not contiguous",
			)
		}
		if len(messages) >= MaxPRDevelopmentMessagesPerCase {
			return loadedPRDevelopmentConversation{}, fmt.Errorf(
				"stored pull request development transcript exceeds %d messages",
				MaxPRDevelopmentMessagesPerCase,
			)
		}
		if int64(len(message.Content)) >
			int64(MaxPRDevelopmentTranscriptBytes)-contentBytes {
			return loadedPRDevelopmentConversation{}, fmt.Errorf(
				"stored pull request development transcript exceeds %d bytes",
				MaxPRDevelopmentTranscriptBytes,
			)
		}
		contentBytes += int64(len(message.Content))
		transcriptDigest, scanErr = extendPRDevelopmentTranscriptDigest(
			transcriptDigest,
			message,
		)
		if scanErr != nil {
			return loadedPRDevelopmentConversation{}, fmt.Errorf(
				"stored pull request development transcript digest is invalid: %w",
				scanErr,
			)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return loadedPRDevelopmentConversation{}, err
	}
	if state.Version != int64(len(messages)) ||
		state.ContentBytes != contentBytes ||
		state.TranscriptDigest != transcriptDigest {
		return loadedPRDevelopmentConversation{}, fmt.Errorf(
			"stored pull request development conversation state does not match its transcript",
		)
	}
	return loadedPRDevelopmentConversation{
		Conversation: PRDevelopmentConversation{
			CaseID:   caseID,
			Version:  state.Version,
			Messages: messages,
		},
		ContentBytes:     contentBytes,
		TranscriptDigest: transcriptDigest,
	}, nil
}

func getPRDevelopmentConversationState(
	ctx context.Context,
	queryer rowQueryer,
	caseID string,
) (storedPRDevelopmentConversationState, error) {
	var state storedPRDevelopmentConversationState
	err := queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentConversationStateColumns+`
		FROM pr_development_conversations
		WHERE case_id = ?`,
		caseID,
	).Scan(
		&state.CaseID,
		&state.Version,
		&state.ContentBytes,
		&state.TranscriptDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedPRDevelopmentConversationState{}, fmt.Errorf(
			"stored pull request development conversation state is missing",
		)
	}
	if err != nil {
		return storedPRDevelopmentConversationState{}, fmt.Errorf(
			"scan stored pull request development conversation state: %w",
			err,
		)
	}
	if state.CaseID != caseID ||
		!validPrefixedHexID(state.CaseID, prDevelopmentCaseIDPrefix) ||
		state.Version < 0 ||
		state.Version > int64(MaxPRDevelopmentMessagesPerCase) ||
		state.ContentBytes < 0 ||
		state.ContentBytes > int64(MaxPRDevelopmentTranscriptBytes) ||
		!validPRDevelopmentHex(state.TranscriptDigest, sha256.Size*2) ||
		(state.Version == 0) != (state.ContentBytes == 0) {
		return storedPRDevelopmentConversationState{}, fmt.Errorf(
			"stored pull request development conversation state is invalid",
		)
	}
	return state, nil
}

func scanPRDevelopmentMessage(scanner rowScanner) (PRDevelopmentMessage, error) {
	var (
		message   PRDevelopmentMessage
		ordinal   int64
		createdAt int64
	)
	if err := scanner.Scan(
		&message.ID,
		&message.CaseID,
		&ordinal,
		&message.Role,
		&message.Content,
		&createdAt,
	); err != nil {
		return PRDevelopmentMessage{}, fmt.Errorf(
			"scan stored pull request development message: %w",
			err,
		)
	}
	convertedOrdinal := int(ordinal)
	if ordinal < 0 || int64(convertedOrdinal) != ordinal ||
		!validPrefixedHexID(message.ID, prDevelopmentMessageIDPrefix) ||
		!validPrefixedHexID(message.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPRDevelopmentMessageRole(message.Role) ||
		validateStoredPRDevelopmentMessageContent(message.Content) != nil {
		return PRDevelopmentMessage{}, fmt.Errorf(
			"stored pull request development message is invalid",
		)
	}
	message.Ordinal = convertedOrdinal
	message.CreatedAt = fromDBTime(createdAt)
	if err := validateDBTimestamp(
		"stored pull request development message creation time",
		message.CreatedAt,
	); err != nil {
		return PRDevelopmentMessage{}, fmt.Errorf(
			"stored pull request development message creation time is invalid: %w",
			err,
		)
	}
	return message, nil
}

func normalizePRDevelopmentMessageContent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateStoredPRDevelopmentMessageContent(value); err != nil {
		return "", fmt.Errorf(
			"%w: invalid development message content: %v",
			ErrInvalidPRDevelopment,
			err,
		)
	}
	return value, nil
}

func validateStoredPRDevelopmentMessageContent(value string) error {
	if value == "" {
		return fmt.Errorf("content is required")
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("content has surrounding whitespace")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("content is not valid UTF-8")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("content contains a NUL byte")
	}
	if len(value) > MaxPRDevelopmentMessageBytes {
		return fmt.Errorf("content exceeds %d bytes", MaxPRDevelopmentMessageBytes)
	}
	return nil
}

func validPRDevelopmentMessageRole(role PRDevelopmentMessageRole) bool {
	return role == PRDevelopmentMessageUser ||
		role == PRDevelopmentMessageAssistant
}

func emptyPRDevelopmentTranscriptDigest() string {
	digest := sha256.New()
	writePRDevelopmentTranscriptDigestPart(
		digest,
		prDevelopmentTranscriptDigestDomain,
	)
	writePRDevelopmentTranscriptDigestPart(digest, "empty")
	return hex.EncodeToString(digest.Sum(nil))
}

// extendPRDevelopmentTranscriptDigest advances a domain-separated rolling
// digest over every canonical durable message field. Length-prefixing each
// field keeps the encoding unambiguous without exposing the digest as an
// authentication primitive.
func extendPRDevelopmentTranscriptDigest(
	previous string,
	message PRDevelopmentMessage,
) (string, error) {
	if !validPRDevelopmentHex(previous, sha256.Size*2) {
		return "", fmt.Errorf("previous transcript digest is invalid")
	}
	digest := sha256.New()
	for _, part := range []string{
		prDevelopmentTranscriptDigestDomain,
		"message",
		previous,
		message.ID,
		message.CaseID,
		strconv.Itoa(message.Ordinal),
		string(message.Role),
		message.Content,
		strconv.FormatInt(message.CreatedAt.UnixNano(), 10),
	} {
		writePRDevelopmentTranscriptDigestPart(digest, part)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writePRDevelopmentTranscriptDigestPart(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func prDevelopmentTranscriptBytes(messages []PRDevelopmentMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	return total
}
