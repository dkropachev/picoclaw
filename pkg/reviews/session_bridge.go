package reviews

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	reviewWorkingContextChannel      = "review"
	reviewWorkingContextAliasPrefix  = "review:agent:"
	reviewWorkingContextBindingLabel = ":binding:"
	reviewWorkingContextVersionLabel = ":version:"
	maxWorkingContextFindings        = 200
	maxWorkingContextConnectorBytes  = 256
	maxWorkingContextRepositoryBytes = 512
	maxWorkingContextWorkflowBytes   = 1024
	maxWorkingContextRevisionBytes   = 256
	maxWorkingContextFileBytes       = 4096
	maxWorkingContextTitleBytes      = 8192
	maxWorkingContextListItems       = 256
)

var reviewWorkingContextScopeDimensions = []string{"review"}

// WorkingContextRuntimeAcquire pins one exact runtime generation and returns
// the requested agent's session store. The release function must keep that
// generation alive through the callback passed to WithWorkingContext.
type WorkingContextRuntimeAcquire func(
	context.Context,
	string,
) (context.Context, session.SessionStore, func(), error)

// WorkingContextRequest selects one existing review case and the exact
// canonical agent that owns every working-context gate for that case.
type WorkingContextRequest struct {
	CaseID  string
	AgentID string
}

// WorkingContext identifies one coherent, verified review transcript
// projection available to a working-context gate callback. CaseVersion fences
// the authoritative SQLite aggregate; SessionRevision fences only its derived
// session view. SessionKey is internal runtime state and is never part of the
// browser-safe review Detail projection.
type WorkingContext struct {
	CaseID          string
	CaseVersion     int64
	AgentID         string
	SessionKey      string
	SessionRevision string
	GateSubject     map[string]any
}

// WorkingContextUse runs while this Service's case projection lock and the
// runtime generation lease are held. Consumers must launch the gate
// synchronously and bind SessionRevision. Another Service using the same local
// runtime store may advance the session. If it advances before the downstream
// exact snapshot read, the expected-revision check fails closed; if it advances
// afterward, the already captured evidence remains frozen at this revision. A
// consumer that requires latest-case admission must also revalidate CaseVersion
// against SQLite at its durable decision boundary.
type WorkingContextUse func(context.Context, WorkingContext) error

type exactWorkingContextSessionStore interface {
	session.SnapshotReader
	session.SnapshotReplacer
	session.ScopeAdmitter
}

type reviewCaseLockSet struct {
	mu      sync.Mutex
	entries map[string]*reviewCaseLock
}

type reviewCaseLock struct {
	token chan struct{}
	refs  int
}

func newReviewCaseLockSet() *reviewCaseLockSet {
	return &reviewCaseLockSet{entries: make(map[string]*reviewCaseLock)}
}

func (locks *reviewCaseLockSet) acquire(
	ctx context.Context,
	caseID string,
) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	locks.mu.Lock()
	entry := locks.entries[caseID]
	if entry == nil {
		entry = &reviewCaseLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		locks.entries[caseID] = entry
	}
	entry.refs++
	locks.mu.Unlock()

	select {
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			locks.releaseReference(caseID, entry)
			return nil, err
		}
	case <-ctx.Done():
		locks.releaseReference(caseID, entry)
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			locks.releaseReference(caseID, entry)
		})
	}, nil
}

func (locks *reviewCaseLockSet) releaseReference(caseID string, entry *reviewCaseLock) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry.refs--
	if entry.refs == 0 && locks.entries[caseID] == entry {
		delete(locks.entries, caseID)
	}
}

// WithWorkingContext materializes and verifies the complete authoritative
// review conversation in one hidden agent-owned session, then invokes use
// while this Service's per-case projection lock and runtime generation remain
// held.
//
// The review SQLite aggregate remains authoritative. The session is a derived,
// lazily rebuildable view used only for history: read_only workflow agents.
func (service *Service) WithWorkingContext(
	ctx context.Context,
	request WorkingContextRequest,
	use WorkingContextUse,
) error {
	if service == nil || service.store == nil || isNilWorkingContextValue(service.store) {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	caseID := strings.TrimSpace(request.CaseID)
	agentID := strings.TrimSpace(request.AgentID)
	if request.CaseID != caseID || !validWorkingContextCaseID(caseID) {
		return fmt.Errorf("%w: review case ID is invalid", ErrInvalidRequest)
	}
	if request.AgentID != agentID || !routing.IsCanonicalAgentID(agentID) {
		return fmt.Errorf("%w: review working-context agent ID is invalid", ErrInvalidRequest)
	}
	if use == nil {
		return fmt.Errorf("%w: review working-context callback is required", ErrInvalidRequest)
	}
	if service.acquireWorkingContextRuntime == nil {
		return fmt.Errorf("%w: review working-context runtime is not configured", ErrUnavailable)
	}

	if service.workingContextLocks == nil {
		return fmt.Errorf("%w: review working-context lock set is unavailable", ErrUnavailable)
	}

	runtimeCtx, rawStore, releaseRuntime, err := service.acquireWorkingContextRuntime(ctx, agentID)
	if err != nil {
		if releaseRuntime != nil {
			releaseRuntime()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: acquire review working-context runtime: %v", ErrUnavailable, err)
	}
	if runtimeCtx == nil || releaseRuntime == nil {
		if releaseRuntime != nil {
			releaseRuntime()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf(
			"%w: review working-context runtime lease is invalid",
			ErrUnavailable,
		)
	}
	defer releaseRuntime()
	if runtimeErr := runtimeCtx.Err(); runtimeErr != nil {
		return runtimeErr
	}

	store, ok := rawStore.(exactWorkingContextSessionStore)
	if !ok || isNilWorkingContextValue(rawStore) || isNilWorkingContextValue(store) {
		return fmt.Errorf(
			"%w: review working-context session store lacks atomic snapshots",
			ErrUnavailable,
		)
	}

	// Runtime admission must precede the per-case lock. A reload pauses new
	// admissions while waiting for callers that already hold a runtime lease;
	// taking the case lock first would let a paused unleased caller block a
	// pre-leased caller that reload is waiting to drain.
	releaseCase, err := service.workingContextLocks.acquire(runtimeCtx, caseID)
	if err != nil {
		return err
	}
	defer releaseCase()

	detail, err := service.store.GetReviewCase(runtimeCtx, caseID)
	if err != nil {
		return err
	}
	if validationErr := validateWorkingContextDetail(caseID, detail); validationErr != nil {
		return fmt.Errorf(
			"%w: invalid stored review working context: %v",
			ErrUnavailable,
			validationErr,
		)
	}

	projected, err := projectWorkingContext(runtimeCtx, store, detail, agentID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return use(runtimeCtx, projected)
}

func isNilWorkingContextValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func projectWorkingContext(
	ctx context.Context,
	store exactWorkingContextSessionStore,
	detail eventing.ReviewCaseDetail,
	agentID string,
) (WorkingContext, error) {
	scope := workingContextScope(detail.Case, agentID)
	canonicalKey := session.BuildSessionKey(scope)
	alias := workingContextAlias(agentID, detail.Case.ID)
	desiredAliases := workingContextAliases(detail.Case, agentID)
	if len(desiredAliases) != 3 {
		return WorkingContext{}, errors.New("review working-context aliases are invalid")
	}
	history, err := workingContextHistory(detail)
	if err != nil {
		return WorkingContext{}, err
	}
	gateSubject, err := workingContextGateSubject(detail)
	if err != nil {
		return WorkingContext{}, err
	}

	_, err = store.AdmitSessionScope(ctx, session.SessionScopeAdmission{
		Key:            canonicalKey,
		Scope:          session.CloneScope(&scope),
		InitialAliases: append([]string(nil), desiredAliases[:2]...),
		Mode:           session.ScopeAdmissionReview,
	})
	if err != nil {
		return WorkingContext{}, fmt.Errorf("admit review working-context scope: %w", err)
	}
	previous, found, err := store.ReadSessionSnapshot(ctx, alias)
	if err != nil {
		return WorkingContext{}, fmt.Errorf("read admitted review working-context: %w", err)
	}
	if !found {
		return WorkingContext{}, errors.New("admitted review working-context is missing")
	}
	if bindingErr := validateWorkingContextBindingOrReservation(
		previous,
		scope,
		canonicalKey,
		desiredAliases,
		detail.Case.Version,
	); bindingErr != nil {
		return WorkingContext{}, bindingErr
	}
	expectedRevision := previous.Revision
	if expectedRevision == "" {
		return WorkingContext{}, errors.New(
			"review working-context session has no compare-and-swap revision",
		)
	}
	replacement := session.SessionSnapshotReplacement{
		Key:              canonicalKey,
		History:          history,
		Summary:          "",
		Scope:            session.CloneScope(&scope),
		Aliases:          append([]string(nil), desiredAliases...),
		ExpectedRevision: expectedRevision,
	}
	replaceErr := store.ReplaceSessionSnapshot(ctx, replacement)
	if replaceErr != nil {
		if errors.Is(replaceErr, context.Canceled) ||
			errors.Is(replaceErr, context.DeadlineExceeded) {
			return WorkingContext{}, replaceErr
		}
		return WorkingContext{}, fmt.Errorf("replace review working-context session: %w", replaceErr)
	}

	verified, verifiedFound, readErr := store.ReadSessionSnapshot(ctx, alias)
	if readErr != nil {
		return WorkingContext{}, fmt.Errorf("verify review working-context session: %w", readErr)
	}
	if !verifiedFound {
		return WorkingContext{}, errors.New("review working-context session was not persisted")
	}
	if verifyErr := validateWorkingContextReplacementReadback(
		verified,
		replacement,
	); verifyErr != nil {
		return WorkingContext{}, verifyErr
	}
	if verified.Revision == expectedRevision {
		return WorkingContext{}, errors.New(
			"review working-context session revision did not advance",
		)
	}

	return WorkingContext{
		CaseID:          detail.Case.ID,
		CaseVersion:     detail.Case.Version,
		AgentID:         agentID,
		SessionKey:      canonicalKey,
		SessionRevision: verified.Revision,
		GateSubject:     gateSubject,
	}, nil
}

func validateWorkingContextReplacementReadback(
	verified session.SessionSnapshot,
	replacement session.SessionSnapshotReplacement,
) error {
	if verified.Key != replacement.Key {
		return errors.New("review working-context session resolved to another key")
	}
	if !reflect.DeepEqual(verified.Scope, replacement.Scope) ||
		!slices.Equal(verified.Aliases, replacement.Aliases) {
		return errors.New("review working-context session metadata did not persist exactly")
	}
	if verified.Summary != replacement.Summary {
		return errors.New("review working-context session summary did not persist exactly")
	}
	if !equalWorkingContextHistory(verified.History, replacement.History) {
		return errors.New("review working-context transcript did not persist exactly")
	}
	if verified.Revision == "" {
		return errors.New("review working-context session revision is missing")
	}
	return nil
}

func workingContextScope(reviewCase eventing.ReviewCase, agentID string) session.SessionScope {
	return session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    agentID,
		Channel:    reviewWorkingContextChannel,
		Account:    routing.DefaultAccountID,
		Dimensions: append([]string(nil), reviewWorkingContextScopeDimensions...),
		Values: map[string]string{
			"review": reviewCase.ID,
		},
	}
}

func workingContextBinding(reviewCase eventing.ReviewCase) string {
	identity := make([]byte, 0, 1024)
	appendText := func(value string) {
		identity = strconv.AppendInt(identity, int64(len(value)), 10)
		identity = append(identity, ':')
		identity = append(identity, value...)
	}
	for _, value := range []string{
		reviewCase.ID,
		reviewCase.EventID,
		reviewCase.DispatchID,
		reviewCase.RunID,
		reviewCase.WorkflowRef,
		reviewCase.WorkflowRevision,
		reviewCase.Connector,
		reviewCase.Repository,
		reviewCase.PullURL,
		reviewCase.BaseSHA,
		reviewCase.HeadSHA,
	} {
		appendText(value)
	}
	appendText(strconv.FormatInt(reviewCase.PullNumber, 10))
	digest := sha256.Sum256(identity)
	return hex.EncodeToString(digest[:])
}

func workingContextAlias(agentID, caseID string) string {
	return reviewWorkingContextAliasPrefix + agentID + ":case:" + caseID
}

// workingContextAliases adds immutable PR identity to the stable owner
// alias without making the canonical key depend on that identity.
func workingContextAliases(
	reviewCase eventing.ReviewCase,
	agentID string,
) []string {
	alias := workingContextAlias(agentID, reviewCase.ID)
	return []string{
		alias,
		alias + reviewWorkingContextBindingLabel + workingContextBinding(reviewCase),
		alias + reviewWorkingContextVersionLabel + strconv.FormatInt(reviewCase.Version, 10),
	}
}

func validateWorkingContextBinding(
	snapshot session.SessionSnapshot,
	expectedScope session.SessionScope,
	canonicalKey string,
	desiredAliases []string,
	maximumVersion int64,
) error {
	if snapshot.Key != canonicalKey {
		return errors.New("review working-context binding resolved to another session")
	}
	if !reflect.DeepEqual(snapshot.Scope, &expectedScope) {
		return errors.New("review working-context binding owner or namespace does not match")
	}
	if len(desiredAliases) != 3 || len(snapshot.Aliases) != 3 ||
		snapshot.Aliases[0] != desiredAliases[0] ||
		snapshot.Aliases[1] != desiredAliases[1] {
		return errors.New("review working-context binding aliases do not match exactly")
	}
	versionText, ok := strings.CutPrefix(
		snapshot.Aliases[2],
		desiredAliases[0]+reviewWorkingContextVersionLabel,
	)
	boundVersion, err := strconv.ParseInt(versionText, 10, 64)
	if !ok || err != nil || strconv.FormatInt(boundVersion, 10) != versionText ||
		boundVersion <= 0 || boundVersion > maximumVersion {
		return errors.New("review working-context binding version is invalid")
	}
	return nil
}

func validateWorkingContextBindingOrReservation(
	snapshot session.SessionSnapshot,
	expectedScope session.SessionScope,
	canonicalKey string,
	desiredAliases []string,
	maximumVersion int64,
) error {
	if len(desiredAliases) == 3 && snapshot.Key == canonicalKey &&
		reflect.DeepEqual(snapshot.Scope, &expectedScope) &&
		len(snapshot.Aliases) == 2 &&
		slices.Equal(snapshot.Aliases, desiredAliases[:2]) &&
		len(snapshot.History) == 0 && snapshot.Summary == "" &&
		snapshot.Revision != "" {
		return nil
	}
	return validateWorkingContextBinding(
		snapshot,
		expectedScope,
		canonicalKey,
		desiredAliases,
		maximumVersion,
	)
}

func workingContextHistory(
	detail eventing.ReviewCaseDetail,
) ([]providers.Message, error) {
	history := make([]providers.Message, len(detail.Messages))
	for index, message := range detail.Messages {
		createdAt := message.CreatedAt
		history[index] = providers.Message{
			Role:      string(message.Role),
			Content:   message.Content,
			CreatedAt: &createdAt,
		}
	}
	return history, nil
}

func workingContextGateSubject(
	detail eventing.ReviewCaseDetail,
) (map[string]any, error) {
	if !workingContextGateSubjectTextFits(detail) {
		return nil, fmt.Errorf(
			"review working-context gate subject exceeds %d bytes",
			workflows.MaxWorkflowGateSubjectBytes,
		)
	}
	reviewCase := cloneCase(detail.Case)
	normalizeWorkingContextCaseTimes(&reviewCase)
	findings := cloneWorkingContextFindings(detail.Findings)
	for index := range findings {
		normalizeWorkingContextFindingTimes(&findings[index])
	}
	messages := make([]map[string]any, len(detail.Messages))
	for index, message := range detail.Messages {
		metadata := map[string]any{
			"id":         message.ID,
			"ordinal":    message.Ordinal,
			"kind":       string(message.Kind),
			"role":       string(message.Role),
			"created_at": message.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if message.FindingID != "" {
			metadata["finding_id"] = message.FindingID
		}
		messages[index] = metadata
	}

	encoded, err := json.Marshal(struct {
		Case     eventing.ReviewCase      `json:"case"`
		Findings []eventing.ReviewFinding `json:"findings"`
		Messages []map[string]any         `json:"messages"`
	}{
		Case:     reviewCase,
		Findings: findings,
		Messages: messages,
	})
	if err != nil {
		return nil, fmt.Errorf("encode review working-context gate subject: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var subject map[string]any
	decodeErr := decoder.Decode(&subject)
	if decodeErr != nil || subject == nil {
		return nil, errors.New("decode review working-context gate subject")
	}
	rawFindings, ok := subject["findings"].([]any)
	if !ok || len(rawFindings) != len(detail.Findings) {
		return nil, errors.New("decode review working-context gate findings")
	}
	for _, rawFinding := range rawFindings {
		finding, findingOK := rawFinding.(map[string]any)
		if !findingOK {
			return nil, errors.New("decode review working-context gate finding")
		}
		if _, exists := finding["line"]; !exists {
			finding["line"] = nil
		}
	}
	bounded, err := json.Marshal(subject)
	if err != nil {
		return nil, fmt.Errorf("validate review working-context gate subject: %w", err)
	}
	if len(bounded) > workflows.MaxWorkflowGateSubjectBytes {
		return nil, fmt.Errorf(
			"review working-context gate subject exceeds %d bytes",
			workflows.MaxWorkflowGateSubjectBytes,
		)
	}
	return subject, nil
}

func workingContextGateSubjectTextFits(detail eventing.ReviewCaseDetail) bool {
	remaining := workflows.MaxWorkflowGateSubjectBytes
	consume := func(values ...string) bool {
		for _, value := range values {
			if len(value) > remaining {
				return false
			}
			remaining -= len(value)
		}
		return true
	}
	reviewCase := detail.Case
	if !consume(
		reviewCase.ID,
		reviewCase.EventID,
		reviewCase.DispatchID,
		reviewCase.RunID,
		reviewCase.WorkflowRef,
		reviewCase.WorkflowRevision,
		reviewCase.Connector,
		reviewCase.Repository,
		reviewCase.PullURL,
		reviewCase.BaseSHA,
		reviewCase.HeadSHA,
		reviewCase.Summary,
		reviewCase.PublicErrorCode,
	) || !consume(reviewCase.Tests...) || !consume(reviewCase.ResidualRisks...) {
		return false
	}
	for _, finding := range detail.Findings {
		if !consume(
			finding.ID,
			finding.CaseID,
			string(finding.State),
			string(finding.Severity),
			finding.Title,
			finding.File,
			finding.Message,
			finding.Evidence,
			finding.Impact,
			finding.Recommendation,
			finding.Validation,
			finding.DroppedReason,
		) {
			return false
		}
	}
	for _, message := range detail.Messages {
		if !consume(
			message.ID,
			message.FindingID,
			string(message.Kind),
			string(message.Role),
		) {
			return false
		}
	}
	return true
}

func cloneWorkingContextFindings(
	findings []eventing.ReviewFinding,
) []eventing.ReviewFinding {
	cloned := append([]eventing.ReviewFinding(nil), findings...)
	for index := range cloned {
		if findings[index].Line != nil {
			line := *findings[index].Line
			cloned[index].Line = &line
		}
		if findings[index].DroppedAt != nil {
			droppedAt := *findings[index].DroppedAt
			cloned[index].DroppedAt = &droppedAt
		}
	}
	return cloned
}

func normalizeWorkingContextCaseTimes(reviewCase *eventing.ReviewCase) {
	if reviewCase == nil {
		return
	}
	reviewCase.CreatedAt = reviewCase.CreatedAt.UTC()
	reviewCase.UpdatedAt = reviewCase.UpdatedAt.UTC()
	normalizeWorkingContextTimePointer(&reviewCase.ResolvedAt)
	normalizeWorkingContextTimePointer(&reviewCase.SubmittedAt)
}

func normalizeWorkingContextFindingTimes(finding *eventing.ReviewFinding) {
	if finding == nil {
		return
	}
	finding.CreatedAt = finding.CreatedAt.UTC()
	finding.UpdatedAt = finding.UpdatedAt.UTC()
	normalizeWorkingContextTimePointer(&finding.DroppedAt)
}

func normalizeWorkingContextTimePointer(value **time.Time) {
	if value == nil || *value == nil {
		return
	}
	normalized := (*value).UTC()
	*value = &normalized
}

func equalWorkingContextHistory(left, right []providers.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		actual := left[index]
		expected := right[index]
		if actual.Role != expected.Role || actual.Content != expected.Content ||
			actual.CreatedAt == nil || expected.CreatedAt == nil ||
			!actual.CreatedAt.Equal(*expected.CreatedAt) {
			return false
		}
		actual.CreatedAt = nil
		expected.CreatedAt = nil
		if !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

func validateWorkingContextDetail(
	caseID string,
	detail eventing.ReviewCaseDetail,
) error {
	reviewCase := detail.Case
	if reviewCase.ID != caseID || reviewCase.Version <= 0 ||
		!validWorkingContextPrefixedHexID(reviewCase.EventID, "ev_") ||
		!validWorkingContextPrefixedHexID(reviewCase.DispatchID, "dsp_") ||
		!validWorkingContextPrefixedHexID(reviewCase.RunID, "wr_") ||
		!validWorkingContextBoundedText(
			reviewCase.Connector,
			true,
			maxWorkingContextConnectorBytes,
		) ||
		len(reviewCase.Repository) > maxWorkingContextRepositoryBytes ||
		!validRepository(reviewCase.Repository) ||
		reviewCase.PullNumber <= 0 || reviewCase.PullNumber > 1<<31-1 ||
		!validPullRequestURL(reviewCase.PullURL) ||
		!validWorkingContextGitOID(reviewCase.BaseSHA) ||
		!validWorkingContextGitOID(reviewCase.HeadSHA) ||
		!validCaseStatus(reviewCase.Status, false) ||
		reviewCase.CreatedAt.IsZero() || reviewCase.UpdatedAt.IsZero() ||
		reviewCase.UpdatedAt.Before(reviewCase.CreatedAt) ||
		!validWorkingContextCaseText(reviewCase) {
		return errors.New("case identity is inconsistent")
	}
	if reviewCase.TotalFindings != len(detail.Findings) ||
		reviewCase.ActiveFindings < 0 ||
		reviewCase.ActiveFindings > reviewCase.TotalFindings ||
		len(detail.Findings) > maxWorkingContextFindings {
		return errors.New("case finding counts are inconsistent")
	}
	resolvedExpected := false
	submittedExpected := false
	switch reviewCase.Status {
	case eventing.ReviewCaseOpen, eventing.ReviewCaseSubmitting:
		if reviewCase.ActiveFindings == 0 {
			return errors.New("case status and active finding count are inconsistent")
		}
	case eventing.ReviewCaseAllDropped:
		resolvedExpected = true
		if reviewCase.ActiveFindings != 0 {
			return errors.New("case status and active finding count are inconsistent")
		}
	case eventing.ReviewCaseSubmissionUnknown, eventing.ReviewCaseStale:
		resolvedExpected = true
		if reviewCase.ActiveFindings == 0 {
			return errors.New("case status and active finding count are inconsistent")
		}
	case eventing.ReviewCaseSubmitted:
		resolvedExpected = true
		submittedExpected = true
		if reviewCase.ActiveFindings == 0 {
			return errors.New("case status and active finding count are inconsistent")
		}
	}
	if !validWorkingContextOptionalTime(
		reviewCase.ResolvedAt,
		reviewCase.CreatedAt,
		reviewCase.UpdatedAt,
		resolvedExpected,
	) || !validWorkingContextOptionalTime(
		reviewCase.SubmittedAt,
		reviewCase.CreatedAt,
		reviewCase.UpdatedAt,
		submittedExpected,
	) {
		return errors.New("case lifecycle timestamps are inconsistent")
	}

	findings := make(map[string]struct{}, len(detail.Findings))
	activeFindings := 0
	for index, finding := range detail.Findings {
		cleanFile := path.Clean(finding.File)
		if finding.CaseID != caseID ||
			!validWorkingContextPrefixedHexID(finding.ID, "prf_") ||
			finding.Ordinal != index || finding.Revision <= 0 ||
			finding.CreatedAt.IsZero() || finding.UpdatedAt.IsZero() ||
			finding.UpdatedAt.Before(finding.CreatedAt) ||
			finding.CreatedAt.Before(reviewCase.CreatedAt) ||
			finding.UpdatedAt.After(reviewCase.UpdatedAt) ||
			!validWorkingContextFindingText(finding) ||
			(finding.File != "" && (strings.HasPrefix(finding.File, "/") ||
				cleanFile == "." || cleanFile == ".." ||
				strings.HasPrefix(cleanFile, "../") || cleanFile != finding.File ||
				strings.Contains(finding.File, `\`))) ||
			(finding.Line != nil && (finding.File == "" ||
				*finding.Line <= 0 || *finding.Line > 1<<31-1)) {
			return errors.New("finding identity is inconsistent")
		}
		if _, duplicate := findings[finding.ID]; duplicate {
			return errors.New("finding identity is duplicated")
		}
		switch finding.Severity {
		case eventing.ReviewSeverityCritical, eventing.ReviewSeverityHigh,
			eventing.ReviewSeverityMedium, eventing.ReviewSeverityLow:
		default:
			return errors.New("finding severity is invalid")
		}
		switch finding.State {
		case eventing.ReviewFindingActive:
			activeFindings++
			if finding.DroppedAt != nil || finding.DroppedReason != "" {
				return errors.New("active finding has dropped state")
			}
		case eventing.ReviewFindingDropped:
			if !validWorkingContextOptionalTime(
				finding.DroppedAt,
				finding.CreatedAt,
				finding.UpdatedAt,
				true,
			) {
				return errors.New("dropped finding timestamp is invalid")
			}
		default:
			return errors.New("finding state is invalid")
		}
		findings[finding.ID] = struct{}{}
	}
	if activeFindings != reviewCase.ActiveFindings {
		return errors.New("case active finding count is inconsistent")
	}
	if len(detail.Messages) > eventing.MaxReviewMessagesPerCase {
		return errors.New("message transcript exceeds its durable count")
	}
	messages := make(map[string]struct{}, len(detail.Messages))
	totalBytes := 0
	for index, message := range detail.Messages {
		if message.CaseID != caseID || message.Ordinal != index ||
			!validWorkingContextPrefixedHexID(message.ID, "prm_") ||
			message.CreatedAt.IsZero() ||
			message.CreatedAt.Before(reviewCase.CreatedAt) ||
			message.CreatedAt.After(reviewCase.UpdatedAt) ||
			!validWorkingContextNormalizedText(message.Content, true) ||
			len(message.Content) > eventing.MaxReviewMessageBytes {
			return errors.New("message identity or content is inconsistent")
		}
		if _, duplicate := messages[message.ID]; duplicate {
			return errors.New("message identity is duplicated")
		}
		messages[message.ID] = struct{}{}
		switch message.Role {
		case eventing.ReviewMessageUser, eventing.ReviewMessageAssistant:
		default:
			return errors.New("message role is invalid")
		}
		switch message.Kind {
		case eventing.ReviewMessageChat, eventing.ReviewMessageRephrase:
		default:
			return errors.New("message kind is invalid")
		}
		if message.Kind == eventing.ReviewMessageRephrase && message.FindingID == "" {
			return errors.New("rephrase message finding identity is missing")
		}
		if message.FindingID != "" {
			if _, exists := findings[message.FindingID]; !exists {
				return errors.New("message finding identity is inconsistent")
			}
		}
		if len(message.Content) > eventing.MaxReviewTranscriptBytes-totalBytes {
			return errors.New("message transcript exceeds its durable bound")
		}
		totalBytes += len(message.Content)
	}
	return nil
}

func validWorkingContextCaseText(reviewCase eventing.ReviewCase) bool {
	if !validWorkingContextBoundedText(
		reviewCase.WorkflowRef,
		true,
		maxWorkingContextWorkflowBytes,
	) || !validWorkingContextBoundedText(
		reviewCase.WorkflowRevision,
		false,
		maxWorkingContextRevisionBytes,
	) || !validWorkingContextBoundedText(
		reviewCase.Summary,
		true,
		eventing.MaxReviewMessageBytes,
	) || !validWorkingContextBoundedText(
		reviewCase.PublicErrorCode,
		false,
		maxWorkingContextRevisionBytes,
	) {
		return false
	}
	for _, values := range [][]string{reviewCase.Tests, reviewCase.ResidualRisks} {
		if len(values) > maxWorkingContextListItems {
			return false
		}
		for _, value := range values {
			if !validWorkingContextBoundedText(
				value,
				true,
				eventing.MaxReviewMessageBytes,
			) {
				return false
			}
		}
	}
	return true
}

func validWorkingContextFindingText(finding eventing.ReviewFinding) bool {
	for _, field := range []struct {
		value    string
		required bool
		maximum  int
	}{
		{finding.Title, true, maxWorkingContextTitleBytes},
		{finding.File, false, maxWorkingContextFileBytes},
		{finding.Message, true, eventing.MaxReviewMessageBytes},
		{finding.Evidence, false, eventing.MaxReviewMessageBytes},
		{finding.Impact, false, eventing.MaxReviewMessageBytes},
		{finding.Recommendation, false, eventing.MaxReviewMessageBytes},
		{finding.Validation, false, eventing.MaxReviewMessageBytes},
		{finding.DroppedReason, false, eventing.MaxReviewMessageBytes},
	} {
		if !validWorkingContextBoundedText(field.value, field.required, field.maximum) {
			return false
		}
	}
	return true
}

func validWorkingContextNormalizedText(value string, required bool) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) &&
		!strings.ContainsRune(value, '\x00') &&
		(!required || value != "")
}

func validWorkingContextBoundedText(value string, required bool, maximum int) bool {
	return len(value) <= maximum && validWorkingContextNormalizedText(value, required)
}

func validWorkingContextOptionalTime(
	value *time.Time,
	minimum time.Time,
	maximum time.Time,
	required bool,
) bool {
	if value == nil {
		return !required
	}
	return required && !value.IsZero() &&
		!value.Before(minimum) && !value.After(maximum)
}

func validWorkingContextCaseID(value string) bool {
	return validWorkingContextPrefixedHexID(value, "prc_")
}

func validWorkingContextPrefixedHexID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validWorkingContextGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
