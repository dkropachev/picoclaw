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
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	prDevelopmentThreadIdentityDigestDomain = "picoclaw-pr-development-thread-identity-v1"
	prDevelopmentThreadCasesDigestDomain    = "picoclaw-pr-development-thread-cases-v1"
	maxPRDevelopmentProviderOriginBytes     = 4096
	prDevelopmentThreadColumns              = `
		id, identity_kind, provider, provider_origin, pull_author_id,
		repository_id, pull_request_id, pull_number, legacy_case_id,
		case_count, identity_hash, cases_digest, created_at, updated_at`
)

var _ PRDevelopmentThreadReader = (*Store)(nil)

// GetPRDevelopmentThreadForCase returns the complete immutable thread and
// validates its identity hash, contiguous capture-hash memberships, and the
// rolling cases digest from one SQLite snapshot without loading case payloads.
func (s *Store) GetPRDevelopmentThreadForCase(
	ctx context.Context,
	caseID string,
) (PRDevelopmentThread, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentThread{}, err
	}
	caseID = strings.TrimSpace(caseID)
	if !validPrefixedHexID(caseID, prDevelopmentCaseIDPrefix) {
		return PRDevelopmentThread{}, fmt.Errorf(
			"%w: invalid development case ID",
			ErrInvalidPRDevelopment,
		)
	}
	var thread PRDevelopmentThread
	err := s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := loadPRDevelopmentThreadForCase(ctx, queryer, caseID)
			thread = loaded
			return loadErr
		},
	)
	if err != nil {
		return PRDevelopmentThread{}, fmt.Errorf(
			"get pull request development thread: %w",
			s.dbError(err),
		)
	}
	return thread, nil
}

func loadPRDevelopmentThreadForCase(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
) (PRDevelopmentThread, error) {
	var threadID string
	err := queryer.QueryRowContext(ctx, `
		SELECT thread_id
		FROM pr_development_thread_cases
		WHERE case_id = ?`,
		caseID,
	).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentThread{}, fmt.Errorf(
			"stored pull request development case has no thread",
		)
	}
	if err != nil {
		return PRDevelopmentThread{}, err
	}
	return loadPRDevelopmentThread(ctx, queryer, threadID)
}

func loadPRDevelopmentThreadBindingForCase(
	ctx context.Context,
	queryer rowsQueryer,
	caseID string,
) (PRDevelopmentThreadBinding, error) {
	var threadID string
	err := queryer.QueryRowContext(ctx, `
		SELECT thread_id
		FROM pr_development_thread_cases
		WHERE case_id = ?`,
		caseID,
	).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored pull request development case has no thread",
		)
	}
	if err != nil {
		return PRDevelopmentThreadBinding{}, err
	}
	thread, err := scanPRDevelopmentThread(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentThreadColumns+`
		FROM pr_development_threads
		WHERE id = ?`,
		threadID,
	))
	if err != nil {
		return PRDevelopmentThreadBinding{}, err
	}
	var linkCount, minimumOrdinal, maximumOrdinal int
	var firstLinkedAt, tailLinkedAt int64
	var tailHash string
	err = queryer.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MIN(ordinal), -1), COALESCE(MAX(ordinal), -1),
			COALESCE((
				SELECT linked_at FROM pr_development_thread_cases
				WHERE thread_id = ? AND ordinal = 0
			), 0),
			COALESCE((
				SELECT link_hash FROM pr_development_thread_cases
				WHERE thread_id = ? AND ordinal = ?
			), ''),
			COALESCE((
				SELECT linked_at FROM pr_development_thread_cases
				WHERE thread_id = ? AND ordinal = ?
			), 0)
		FROM pr_development_thread_cases
		WHERE thread_id = ?`,
		threadID,
		threadID,
		thread.CaseCount-1,
		threadID,
		thread.CaseCount-1,
		threadID,
	).Scan(
		&linkCount,
		&minimumOrdinal,
		&maximumOrdinal,
		&firstLinkedAt,
		&tailHash,
		&tailLinkedAt,
	)
	if err != nil {
		return PRDevelopmentThreadBinding{}, err
	}
	if linkCount != thread.CaseCount || minimumOrdinal != 0 ||
		maximumOrdinal != thread.CaseCount-1 ||
		!fromDBTime(firstLinkedAt).Equal(thread.CreatedAt) ||
		tailHash != thread.CasesDigest ||
		!fromDBTime(tailLinkedAt).Equal(thread.UpdatedAt) {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored pull request development thread high-water state is invalid",
		)
	}
	var link PRDevelopmentThreadCaseLink
	var linkedAt int64
	err = queryer.QueryRowContext(ctx, `
		SELECT case_id, ordinal, linked_at, previous_hash, link_hash
		FROM pr_development_thread_cases
		WHERE case_id = ? AND thread_id = ?`,
		caseID,
		threadID,
	).Scan(
		&link.CaseID,
		&link.Ordinal,
		&linkedAt,
		&link.PreviousHash,
		&link.LinkHash,
	)
	if err != nil {
		return PRDevelopmentThreadBinding{}, err
	}
	link.LinkedAt = fromDBTime(linkedAt)
	if link.CaseID != caseID || link.Ordinal < 0 || link.Ordinal >= thread.CaseCount ||
		!validPRDevelopmentHex(link.PreviousHash, sha256.Size*2) ||
		!validPRDevelopmentHex(link.LinkHash, sha256.Size*2) || link.LinkedAt.IsZero() ||
		(link.Ordinal == 0 &&
			link.PreviousHash != emptyPRDevelopmentThreadCasesDigest()) ||
		(link.Ordinal == 0 && !link.LinkedAt.Equal(thread.CreatedAt)) ||
		(link.Ordinal == thread.CaseCount-1 &&
			(link.LinkHash != thread.CasesDigest ||
				!link.LinkedAt.Equal(thread.UpdatedAt))) {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored pull request development thread binding is invalid",
		)
	}
	var predecessorHash, successorPreviousHash sql.NullString
	err = queryer.QueryRowContext(ctx, `
		SELECT
			(SELECT link_hash FROM pr_development_thread_cases
			 WHERE thread_id = ? AND ordinal = ?),
			(SELECT previous_hash FROM pr_development_thread_cases
			 WHERE thread_id = ? AND ordinal = ?)`,
		threadID,
		link.Ordinal-1,
		threadID,
		link.Ordinal+1,
	).Scan(&predecessorHash, &successorPreviousHash)
	if err != nil {
		return PRDevelopmentThreadBinding{}, err
	}
	if (link.Ordinal == 0 && predecessorHash.Valid) ||
		(link.Ordinal > 0 &&
			(!predecessorHash.Valid || predecessorHash.String != link.PreviousHash)) ||
		(link.Ordinal == thread.CaseCount-1 && successorPreviousHash.Valid) ||
		(link.Ordinal < thread.CaseCount-1 &&
			(!successorPreviousHash.Valid || successorPreviousHash.String != link.LinkHash)) {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored pull request development thread binding adjacency is invalid",
		)
	}
	var captureHash string
	err = queryer.QueryRowContext(ctx, `
		SELECT capture_hash FROM pr_development_cases WHERE id = ?`,
		caseID,
	).Scan(&captureHash)
	if err != nil {
		return PRDevelopmentThreadBinding{}, err
	}
	if !validPRDevelopmentHex(captureHash, sha256.Size*2) {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored pull request development case capture hash is invalid",
		)
	}
	expected, err := extendPRDevelopmentThreadCasesDigest(
		thread.ID,
		thread.IdentityHash,
		captureHash,
		link,
	)
	if err != nil || expected != link.LinkHash {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored pull request development thread binding hash is invalid",
		)
	}
	if thread.Kind == PRDevelopmentThreadLegacy &&
		(thread.CaseCount != 1 || thread.LegacyCaseID != caseID ||
			link.Ordinal != 0) {
		return PRDevelopmentThreadBinding{}, errors.New(
			"stored legacy pull request development thread binding is not isolated",
		)
	}
	return PRDevelopmentThreadBinding{
		ID:           thread.ID,
		Kind:         thread.Kind,
		Identity:     thread.Identity,
		LegacyCaseID: thread.LegacyCaseID,
		CaseCount:    thread.CaseCount,
		IdentityHash: thread.IdentityHash,
		CasesDigest:  thread.CasesDigest,
		Case:         link,
		CreatedAt:    thread.CreatedAt,
		UpdatedAt:    thread.UpdatedAt,
	}, nil
}

func loadPRDevelopmentThread(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
) (PRDevelopmentThread, error) {
	return loadPRDevelopmentThreadAppendState(ctx, queryer, threadID)
}

// loadPRDevelopmentThreadAppendState validates the complete rolling chain
// using only bounded link metadata and each case's fixed-size capture hash. It
// deliberately avoids loading review feedback or other case payload columns.
func loadPRDevelopmentThreadAppendState(
	ctx context.Context,
	queryer rowsQueryer,
	threadID string,
) (PRDevelopmentThread, error) {
	thread, err := scanPRDevelopmentThread(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentThreadColumns+`
		FROM pr_development_threads
		WHERE id = ?`,
		threadID,
	))
	if err != nil {
		return PRDevelopmentThread{}, err
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT link.case_id, link.ordinal, link.linked_at,
			link.previous_hash, link.link_hash, development_case.capture_hash
		FROM pr_development_thread_cases link
		JOIN pr_development_cases development_case ON development_case.id = link.case_id
		WHERE link.thread_id = ?
		ORDER BY link.ordinal`,
		threadID,
	)
	if err != nil {
		return PRDevelopmentThread{}, err
	}
	defer func() { _ = rows.Close() }()
	links := make([]PRDevelopmentThreadCaseLink, 0, thread.CaseCount)
	captureHashes := make([]string, 0, thread.CaseCount)
	for rows.Next() {
		var link PRDevelopmentThreadCaseLink
		var linkedAt int64
		var captureHash string
		if scanErr := rows.Scan(
			&link.CaseID,
			&link.Ordinal,
			&linkedAt,
			&link.PreviousHash,
			&link.LinkHash,
			&captureHash,
		); scanErr != nil {
			return PRDevelopmentThread{}, scanErr
		}
		link.LinkedAt = fromDBTime(linkedAt)
		links = append(links, link)
		captureHashes = append(captureHashes, captureHash)
	}
	if err = rows.Err(); err != nil {
		return PRDevelopmentThread{}, err
	}
	if err = rows.Close(); err != nil {
		return PRDevelopmentThread{}, err
	}
	if len(links) != thread.CaseCount || len(links) == 0 ||
		len(links) > MaxPRDevelopmentThreadCases {
		return PRDevelopmentThread{}, errors.New(
			"stored pull request development thread append count is invalid",
		)
	}
	previous := emptyPRDevelopmentThreadCasesDigest()
	for ordinal := range links {
		link := links[ordinal]
		if link.Ordinal != ordinal ||
			!validPrefixedHexID(link.CaseID, prDevelopmentCaseIDPrefix) ||
			!validPRDevelopmentHex(captureHashes[ordinal], sha256.Size*2) ||
			link.PreviousHash != previous || link.LinkedAt.IsZero() {
			return PRDevelopmentThread{}, errors.New(
				"stored pull request development thread append link is invalid",
			)
		}
		expected, hashErr := extendPRDevelopmentThreadCasesDigest(
			thread.ID,
			thread.IdentityHash,
			captureHashes[ordinal],
			link,
		)
		if hashErr != nil || expected != link.LinkHash {
			return PRDevelopmentThread{}, errors.New(
				"stored pull request development thread append hash is invalid",
			)
		}
		previous = link.LinkHash
	}
	if previous != thread.CasesDigest ||
		!links[0].LinkedAt.Equal(thread.CreatedAt) ||
		!links[len(links)-1].LinkedAt.Equal(thread.UpdatedAt) {
		return PRDevelopmentThread{}, errors.New(
			"stored pull request development thread append state is invalid",
		)
	}
	if thread.Kind == PRDevelopmentThreadLegacy &&
		(len(links) != 1 || links[0].CaseID != thread.LegacyCaseID) {
		return PRDevelopmentThread{}, errors.New(
			"stored legacy pull request development thread append state is invalid",
		)
	}
	thread.Cases = links
	return thread, nil
}

func scanPRDevelopmentThread(scanner rowScanner) (PRDevelopmentThread, error) {
	var thread PRDevelopmentThread
	var provider, origin, authorID, repositoryID, pullID, legacyCaseID sql.NullString
	var pullNumber sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&thread.ID,
		&thread.Kind,
		&provider,
		&origin,
		&authorID,
		&repositoryID,
		&pullID,
		&pullNumber,
		&legacyCaseID,
		&thread.CaseCount,
		&thread.IdentityHash,
		&thread.CasesDigest,
		&createdAt,
		&updatedAt,
	); err != nil {
		return PRDevelopmentThread{}, err
	}
	thread.CreatedAt = fromDBTime(createdAt)
	thread.UpdatedAt = fromDBTime(updatedAt)
	if !validPrefixedHexID(thread.ID, prDevelopmentThreadIDPrefix) ||
		thread.CaseCount < 1 || thread.CaseCount > MaxPRDevelopmentThreadCases ||
		!validPRDevelopmentHex(thread.IdentityHash, sha256.Size*2) ||
		!validPRDevelopmentHex(thread.CasesDigest, sha256.Size*2) ||
		thread.CreatedAt.IsZero() || thread.UpdatedAt.Before(thread.CreatedAt) {
		return PRDevelopmentThread{}, errors.New(
			"stored pull request development thread state is invalid",
		)
	}
	switch thread.Kind {
	case PRDevelopmentThreadProvider:
		if !provider.Valid || !origin.Valid || !authorID.Valid ||
			!repositoryID.Valid || !pullID.Valid || !pullNumber.Valid ||
			legacyCaseID.Valid {
			return PRDevelopmentThread{}, errors.New(
				"stored provider pull request development thread identity is incomplete",
			)
		}
		thread.Identity = PRDevelopmentThreadIdentity{
			Provider:       provider.String,
			ProviderOrigin: origin.String,
			PullAuthorID:   authorID.String,
			RepositoryID:   repositoryID.String,
			PullRequestID:  pullID.String,
			PullNumber:     pullNumber.Int64,
		}
		normalized, normalizeErr := normalizePRDevelopmentThreadIdentity(
			thread.Identity,
			thread.Identity.PullNumber,
			"",
		)
		if normalizeErr != nil || normalized != thread.Identity ||
			prDevelopmentProviderThreadIdentityHash(normalized) != thread.IdentityHash {
			return PRDevelopmentThread{}, errors.New(
				"stored provider pull request development thread identity is invalid",
			)
		}
	case PRDevelopmentThreadLegacy:
		if provider.Valid || origin.Valid || authorID.Valid || repositoryID.Valid ||
			pullID.Valid || pullNumber.Valid || !legacyCaseID.Valid ||
			!validPrefixedHexID(legacyCaseID.String, prDevelopmentCaseIDPrefix) ||
			thread.CaseCount != 1 {
			return PRDevelopmentThread{}, errors.New(
				"stored legacy pull request development thread identity is invalid",
			)
		}
		thread.LegacyCaseID = legacyCaseID.String
		if prDevelopmentLegacyThreadIdentityHash(thread.LegacyCaseID) != thread.IdentityHash {
			return PRDevelopmentThread{}, errors.New(
				"stored legacy pull request development thread hash is invalid",
			)
		}
	default:
		return PRDevelopmentThread{}, errors.New(
			"stored pull request development thread kind is invalid",
		)
	}
	return thread, nil
}

func normalizePRDevelopmentThreadIdentity(
	identity PRDevelopmentThreadIdentity,
	pullNumber int64,
	pullURL string,
) (PRDevelopmentThreadIdentity, error) {
	identity.Provider = strings.TrimSpace(identity.Provider)
	identity.ProviderOrigin = strings.TrimSpace(identity.ProviderOrigin)
	identity.PullAuthorID = strings.TrimSpace(identity.PullAuthorID)
	identity.RepositoryID = strings.TrimSpace(identity.RepositoryID)
	identity.PullRequestID = strings.TrimSpace(identity.PullRequestID)
	if identity.Provider != "github" ||
		!validPRDevelopmentProviderOrigin(identity.ProviderOrigin) ||
		!validPRDevelopmentDecimalID(identity.PullAuthorID) ||
		!validPRDevelopmentDecimalID(identity.RepositoryID) ||
		!validPRDevelopmentDecimalID(identity.PullRequestID) ||
		identity.PullNumber <= 0 || identity.PullNumber > maxReviewPullNumber ||
		identity.PullNumber != pullNumber {
		return PRDevelopmentThreadIdentity{}, fmt.Errorf(
			"%w: provider thread identity is invalid",
			ErrInvalidPRDevelopment,
		)
	}
	if pullURL != "" {
		parsed, err := url.Parse(pullURL)
		if err != nil || parsed.Scheme+"://"+parsed.Host != identity.ProviderOrigin {
			return PRDevelopmentThreadIdentity{}, fmt.Errorf(
				"%w: provider origin does not match the pull URL",
				ErrInvalidPRDevelopment,
			)
		}
	}
	return identity, nil
}

func validPRDevelopmentProviderOrigin(value string) bool {
	if value == "" || len(value) > maxPRDevelopmentProviderOriginBytes ||
		!utf8.ValidString(value) || value != strings.TrimSpace(value) ||
		strings.ContainsRune(value, '\x00') {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.Opaque != "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.Host != strings.ToLower(parsed.Host) ||
		parsed.String() != value || strings.HasSuffix(parsed.Host, ":") {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return false
	}
	if port := parsed.Port(); port != "" {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 ||
			parsedPort == 443 || strconv.Itoa(parsedPort) != port {
			return false
		}
	}
	if address, parseErr := netip.ParseAddr(hostname); parseErr == nil {
		return address.Zone() == "" && !address.Is4In6() &&
			address.String() == hostname
	}
	if strings.Contains(hostname, ":") ||
		looksLikeNoncanonicalPRDevelopmentIPAddress(hostname) {
		return false
	}
	if len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' ||
			label[len(label)-1] == '-' {
			return false
		}
		for _, character := range []byte(label) {
			if character >= 'a' && character <= 'z' ||
				character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func looksLikeNoncanonicalPRDevelopmentIPAddress(hostname string) bool {
	// Some system resolvers accept inet_aton decimal, octal, and 0x-prefixed
	// components as IPv4 literals even though netip correctly rejects them.
	for _, component := range strings.Split(hostname, ".") {
		if component == "" {
			return true
		}
		digits := component
		hexadecimal := strings.HasPrefix(component, "0x")
		if hexadecimal {
			digits = component[2:]
			if digits == "" {
				return false
			}
		}
		for _, character := range []byte(digits) {
			if character >= '0' && character <= '9' {
				continue
			}
			if hexadecimal && character >= 'a' && character <= 'f' {
				continue
			}
			return false
		}
	}
	return true
}

func prDevelopmentProviderThreadIdentityHash(
	identity PRDevelopmentThreadIdentity,
) string {
	return prDevelopmentThreadDigest(
		prDevelopmentThreadIdentityDigestDomain,
		"provider",
		identity.Provider,
		identity.ProviderOrigin,
		identity.PullAuthorID,
		identity.RepositoryID,
		identity.PullRequestID,
		strconv.FormatInt(identity.PullNumber, 10),
	)
}

func prDevelopmentLegacyThreadIdentityHash(caseID string) string {
	return prDevelopmentThreadDigest(
		prDevelopmentThreadIdentityDigestDomain,
		"legacy",
		caseID,
	)
}

func emptyPRDevelopmentThreadCasesDigest() string {
	return prDevelopmentThreadDigest(
		prDevelopmentThreadCasesDigestDomain,
		"empty",
	)
}

func extendPRDevelopmentThreadCasesDigest(
	threadID, identityHash, captureHash string,
	link PRDevelopmentThreadCaseLink,
) (string, error) {
	if !validPrefixedHexID(threadID, prDevelopmentThreadIDPrefix) ||
		!validPRDevelopmentHex(identityHash, sha256.Size*2) ||
		!validPRDevelopmentHex(captureHash, sha256.Size*2) ||
		!validPRDevelopmentHex(link.PreviousHash, sha256.Size*2) ||
		!validPrefixedHexID(link.CaseID, prDevelopmentCaseIDPrefix) ||
		link.Ordinal < 0 || link.Ordinal >= MaxPRDevelopmentThreadCases ||
		link.LinkedAt.IsZero() {
		return "", errors.New("pull request development thread link input is invalid")
	}
	return prDevelopmentThreadDigest(
		prDevelopmentThreadCasesDigestDomain,
		"case",
		link.PreviousHash,
		threadID,
		identityHash,
		captureHash,
		link.CaseID,
		strconv.Itoa(link.Ordinal),
		strconv.FormatInt(link.LinkedAt.UnixNano(), 10),
	), nil
}

func prDevelopmentThreadDigest(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		writePRDevelopmentThreadDigestPart(digest, part)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writePRDevelopmentThreadDigestPart(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func findPRDevelopmentProviderThread(
	ctx context.Context,
	queryer rowsQueryer,
	identity PRDevelopmentThreadIdentity,
) (PRDevelopmentThread, bool, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT id
		FROM pr_development_threads
		WHERE identity_kind = 'provider' AND provider = ? AND provider_origin = ?
			AND (pull_request_id = ? OR (repository_id = ? AND pull_number = ?))
		ORDER BY id`,
		identity.Provider,
		identity.ProviderOrigin,
		identity.PullRequestID,
		identity.RepositoryID,
		identity.PullNumber,
	)
	if err != nil {
		return PRDevelopmentThread{}, false, err
	}
	defer func() { _ = rows.Close() }()
	threadIDs := make([]string, 0, 2)
	for rows.Next() {
		var threadID string
		if scanErr := rows.Scan(&threadID); scanErr != nil {
			return PRDevelopmentThread{}, false, scanErr
		}
		threadIDs = append(threadIDs, threadID)
	}
	if err = rows.Err(); err != nil {
		return PRDevelopmentThread{}, false, err
	}
	if err = rows.Close(); err != nil {
		return PRDevelopmentThread{}, false, err
	}
	if len(threadIDs) == 0 {
		return PRDevelopmentThread{}, false, nil
	}
	if len(threadIDs) != 1 {
		return PRDevelopmentThread{}, false, fmt.Errorf(
			"%w: provider pull identity resolves to multiple threads",
			ErrPRDevelopmentConflict,
		)
	}
	thread, err := loadPRDevelopmentThreadAppendState(ctx, queryer, threadIDs[0])
	if err != nil {
		return PRDevelopmentThread{}, false, err
	}
	if thread.Kind != PRDevelopmentThreadProvider || thread.Identity != identity {
		return PRDevelopmentThread{}, false, fmt.Errorf(
			"%w: provider pull identity differs from the stored thread",
			ErrPRDevelopmentConflict,
		)
	}
	return thread, true, nil
}

func preparePRDevelopmentProviderThreadLink(
	ctx context.Context,
	conn *sql.Conn,
	identity PRDevelopmentThreadIdentity,
	caseID, captureHash string,
	linkedAt time.Time,
) (string, PRDevelopmentThreadCaseLink, error) {
	thread, found, err := findPRDevelopmentProviderThread(ctx, conn, identity)
	if err != nil {
		return "", PRDevelopmentThreadCaseLink{}, err
	}
	identityHash := prDevelopmentProviderThreadIdentityHash(identity)
	if !found {
		thread.ID, err = newPrefixedID(prDevelopmentThreadIDPrefix)
		if err != nil {
			return "", PRDevelopmentThreadCaseLink{}, err
		}
		thread.CasesDigest = emptyPRDevelopmentThreadCasesDigest()
		thread.CaseCount = 0
	} else if err = checkPRDevelopmentThreadAppendCapacity(thread.CaseCount); err != nil {
		return "", PRDevelopmentThreadCaseLink{}, err
	}
	link := PRDevelopmentThreadCaseLink{
		CaseID:       caseID,
		Ordinal:      thread.CaseCount,
		LinkedAt:     linkedAt,
		PreviousHash: thread.CasesDigest,
	}
	link.LinkHash, err = extendPRDevelopmentThreadCasesDigest(
		thread.ID,
		identityHash,
		captureHash,
		link,
	)
	if err != nil {
		return "", PRDevelopmentThreadCaseLink{}, err
	}
	if !found {
		_, err = conn.ExecContext(ctx, `
			INSERT INTO pr_development_threads (
				id, identity_kind, provider, provider_origin, pull_author_id,
				repository_id, pull_request_id, pull_number, case_count,
				identity_hash, cases_digest, created_at, updated_at
			) VALUES (?, 'provider', ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
			thread.ID,
			identity.Provider,
			identity.ProviderOrigin,
			identity.PullAuthorID,
			identity.RepositoryID,
			identity.PullRequestID,
			identity.PullNumber,
			identityHash,
			link.LinkHash,
			toDBTime(linkedAt),
			toDBTime(linkedAt),
		)
		if err != nil {
			return "", PRDevelopmentThreadCaseLink{}, err
		}
		return thread.ID, link, nil
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE pr_development_threads
		SET case_count = ?, cases_digest = ?, updated_at = ?
		WHERE id = ? AND case_count = ? AND cases_digest = ? AND identity_hash = ?`,
		thread.CaseCount+1,
		link.LinkHash,
		toDBTime(linkedAt),
		thread.ID,
		thread.CaseCount,
		thread.CasesDigest,
		identityHash,
	)
	if err != nil {
		return "", PRDevelopmentThreadCaseLink{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", PRDevelopmentThreadCaseLink{}, err
	}
	if affected != 1 {
		return "", PRDevelopmentThreadCaseLink{}, errors.New(
			"stored pull request development thread changed unexpectedly",
		)
	}
	return thread.ID, link, nil
}

func checkPRDevelopmentThreadAppendCapacity(caseCount int) error {
	if caseCount < 0 || caseCount >= MaxPRDevelopmentThreadCases {
		return fmt.Errorf(
			"%w: thread cannot exceed %d cases",
			ErrPRDevelopmentThreadCapacity,
			MaxPRDevelopmentThreadCases,
		)
	}
	return nil
}

func validatePRDevelopmentThreadCoverage(ctx context.Context, conn *sql.Conn) error {
	var cases, links int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pr_development_cases`).Scan(&cases); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pr_development_thread_cases`).Scan(&links); err != nil {
		return err
	}
	if links != cases {
		return fmt.Errorf(
			"pull request development thread coverage is incomplete: cases=%d links=%d",
			cases,
			links,
		)
	}
	rows, err := conn.QueryContext(ctx, `SELECT id FROM pr_development_threads ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	threadIDs := make([]string, 0)
	for rows.Next() {
		var threadID string
		if scanErr := rows.Scan(&threadID); scanErr != nil {
			return scanErr
		}
		threadIDs = append(threadIDs, threadID)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, threadID := range threadIDs {
		if _, loadErr := loadPRDevelopmentThread(ctx, conn, threadID); loadErr != nil {
			return loadErr
		}
	}
	return nil
}
