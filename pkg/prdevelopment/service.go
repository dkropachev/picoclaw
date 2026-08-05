package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	DefaultCaseListLimit = 50
	MaximumCaseListLimit = 100
	// MaximumRepositoryBytes is the durable repository-identity bound.
	MaximumRepositoryBytes = 256
	// MaximumPullNumber is the durable GitHub pull-request number bound.
	MaximumPullNumber int64 = 1<<31 - 1
)

var (
	// ErrInvalidRequest is safe for the protected HTTP layer to map to 400.
	ErrInvalidRequest = errors.New("invalid pull request development request")
	// ErrUnavailable reports a missing or unusable read service.
	ErrUnavailable = errors.New("pull request development service is unavailable")
)

// Reader is the narrow immutable boundary exposed by the development
// workbench. Capture provenance and every mutation capability remain outside
// this interface.
type Reader interface {
	ListPRDevelopmentCases(
		ctx context.Context,
		filter eventing.PRDevelopmentCaseFilter,
	) (eventing.PRDevelopmentCasePage, error)
	GetPRDevelopmentCase(ctx context.Context, id string) (eventing.PRDevelopmentCase, error)
}

// Service projects immutable captures into deliberately bounded browser DTOs.
type Service struct {
	store Reader
}

func NewService(store Reader) (*Service, error) {
	if store == nil {
		return nil, errors.New("pull request development store is required")
	}
	return &Service{store: store}, nil
}

type ListRequest struct {
	Repository string
	PullNumber int64
	Limit      int
	Cursor     string
}

// CaseSummary is the complete list projection. Provider state and SHAs are
// snapshots captured at CapturedAt; they are never current authority.
type CaseSummary struct {
	ID                   string                            `json:"id"`
	Repository           string                            `json:"repository"`
	PullNumber           int64                             `json:"pull_number"`
	PullURL              string                            `json:"pull_url"`
	PullAuthor           string                            `json:"pull_author"`
	PullState            eventing.PRDevelopmentPullState   `json:"pull_state"`
	PullDraft            bool                              `json:"pull_draft"`
	PullMerged           bool                              `json:"pull_merged"`
	HeadRepository       string                            `json:"head_repository"`
	HeadRef              string                            `json:"head_ref"`
	HeadSHA              string                            `json:"head_sha"`
	ReviewAuthor         string                            `json:"review_author"`
	SubmittedReviewState eventing.PRDevelopmentReviewState `json:"submitted_review_state"`
	CurrentReviewState   eventing.PRDevelopmentReviewState `json:"current_review_state"`
	ReviewSubmittedAt    time.Time                         `json:"review_submitted_at"`
	ReviewURL            string                            `json:"review_url"`
	CapturedAt           time.Time                         `json:"captured_at"`
}

// CaseDetail adds only the captured evidence needed by the local workbench.
// Event, dispatch, workflow, connector, target-user, and provider review IDs
// are intentionally unrepresentable here.
type CaseDetail struct {
	CaseSummary
	BaseRepository  string `json:"base_repository"`
	BaseRef         string `json:"base_ref"`
	BaseSHA         string `json:"base_sha"`
	ReviewCommitSHA string `json:"review_commit_sha"`
	Feedback        string `json:"feedback"`
}

type Page struct {
	Cases      []CaseSummary `json:"cases"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type Detail struct {
	Case CaseDetail `json:"case"`
}

func (service *Service) List(ctx context.Context, request ListRequest) (Page, error) {
	if service == nil || service.store == nil {
		return Page{}, ErrUnavailable
	}
	repository, err := normalizeRepositoryFilter(request.Repository)
	if err != nil {
		return Page{}, err
	}
	if request.PullNumber < 0 || request.PullNumber > MaximumPullNumber {
		return Page{}, fmt.Errorf("%w: pull number is invalid", ErrInvalidRequest)
	}
	limit, err := normalizeCaseListLimit(request.Limit)
	if err != nil {
		return Page{}, err
	}
	filter := cursorFilter{Repository: repository, PullNumber: request.PullNumber}
	after, err := decodeCaseCursor(request.Cursor, filter)
	if err != nil {
		return Page{}, err
	}
	stored, err := service.store.ListPRDevelopmentCases(
		ctx,
		eventing.PRDevelopmentCaseFilter{
			Repository: repository,
			PullNumber: request.PullNumber,
			After:      after,
			Limit:      limit,
		},
	)
	if err != nil {
		return Page{}, err
	}
	page := Page{Cases: make([]CaseSummary, len(stored.Cases))}
	for index := range stored.Cases {
		page.Cases[index] = projectCaseSummary(stored.Cases[index])
	}
	if stored.Next != nil {
		page.NextCursor, err = encodeCaseCursor(*stored.Next, filter)
		if err != nil {
			return Page{}, fmt.Errorf("%w: encode cursor", ErrUnavailable)
		}
	}
	return page, nil
}

func (service *Service) Get(ctx context.Context, caseID string) (Detail, error) {
	if service == nil || service.store == nil {
		return Detail{}, ErrUnavailable
	}
	if !validCaseID(caseID) {
		return Detail{}, fmt.Errorf("%w: case ID is invalid", ErrInvalidRequest)
	}
	stored, err := service.store.GetPRDevelopmentCase(ctx, caseID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Case: projectCaseDetail(stored)}, nil
}

func normalizeCaseListLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultCaseListLimit, nil
	}
	if limit < 1 || limit > MaximumCaseListLimit {
		return 0, fmt.Errorf("%w: limit is invalid", ErrInvalidRequest)
	}
	return limit, nil
}

func normalizeRepositoryFilter(repository string) (string, error) {
	if repository == "" {
		return "", nil
	}
	if !utf8.ValidString(repository) ||
		repository != strings.TrimSpace(repository) ||
		len(repository) > MaximumRepositoryBytes ||
		!repositoryPattern.MatchString(repository) {
		return "", fmt.Errorf("%w: repository is invalid", ErrInvalidRequest)
	}
	return repository, nil
}

func projectCaseSummary(stored eventing.PRDevelopmentCase) CaseSummary {
	return CaseSummary{
		ID:                   stored.ID,
		Repository:           stored.Repository,
		PullNumber:           stored.PullNumber,
		PullURL:              stored.PullURL,
		PullAuthor:           stored.PullAuthor,
		PullState:            stored.PullState,
		PullDraft:            stored.PullDraft,
		PullMerged:           stored.PullMerged,
		HeadRepository:       stored.HeadRepository,
		HeadRef:              stored.HeadRef,
		HeadSHA:              stored.HeadSHA,
		ReviewAuthor:         stored.ReviewAuthor,
		SubmittedReviewState: stored.SubmittedReviewState,
		CurrentReviewState:   stored.CurrentReviewState,
		ReviewSubmittedAt:    stored.ReviewSubmittedAt,
		ReviewURL:            stored.ReviewURL,
		CapturedAt:           stored.CreatedAt,
	}
}

func projectCaseDetail(stored eventing.PRDevelopmentCase) CaseDetail {
	return CaseDetail{
		CaseSummary:     projectCaseSummary(stored),
		BaseRepository:  stored.BaseRepository,
		BaseRef:         stored.BaseRef,
		BaseSHA:         stored.BaseSHA,
		ReviewCommitSHA: stored.ReviewCommitSHA,
		Feedback:        stored.Feedback,
	}
}

func validCaseID(value string) bool {
	const prefix = "pdc_"
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
