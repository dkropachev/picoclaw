package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	SessionsBrokerDomain  = "sessions"
	SessionsBrokerVersion = 1

	sessionOperationPing             = "session.ping"
	SessionOperationResolveStore     = "session.resolve-store"
	sessionOperationAdd              = "session.add"
	sessionOperationHistory          = "session.history"
	sessionOperationGetSummary       = "session.get-summary"
	sessionOperationSetSummary       = "session.set-summary"
	sessionOperationSetHistory       = "session.set-history"
	sessionOperationTruncate         = "session.truncate"
	sessionOperationCompact          = "session.compact"
	sessionOperationList             = "session.list"
	sessionOperationResolve          = "session.resolve"
	sessionOperationGetMeta          = "session.get-meta"
	sessionOperationUpsertMeta       = "session.upsert-meta"
	sessionOperationReadState        = "session.read-state"
	sessionOperationReplaceSnapshot  = "session.replace-snapshot"
	sessionOperationPromoteAlias     = "session.promote-alias"
	sessionOperationEnsure           = "session.ensure"
	sessionOperationDelete           = "session.delete"
	sessionOperationCompareSwapMeta  = "session.compare-swap-meta"
	sessionOperationCompareDelete    = "session.compare-delete"
	sessionOperationReadMutationMeta = "session.read-mutation-meta"
	sessionOperationApplyMeta        = "session.apply-meta"
	sessionOperationApplyAdmission   = "session.apply-admission"

	sessionHistoryPageLimit = 8
	sessionListPageLimit    = 256
	sessionCallbackRetries  = 8
)

type sessionStoreRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

// StoreResolutionRequest identifies a constructor workspace through a
// one-way opaque selector. It contains no path or filename.
type StoreResolutionRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

// StoreResolutionResponse returns only a trusted catalog StoreID.
type StoreResolutionResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type sessionKeyRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Key     string           `json:"key"`
}

type sessionAddRequest struct {
	StoreID database.StoreID  `json:"store_id"`
	Key     string            `json:"key"`
	Message providers.Message `json:"message"`
}

type sessionHistoryRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Key     string           `json:"key"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

type sessionReadStateRequest struct {
	StoreID          database.StoreID `json:"store_id"`
	Key              string           `json:"key"`
	Offset           int              `json:"offset"`
	Limit            int              `json:"limit"`
	ExpectedRevision string           `json:"expected_revision,omitempty"`
}

type sessionSummaryRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Key     string           `json:"key"`
	Summary string           `json:"summary"`
}

type sessionHistoryMutationRequest struct {
	StoreID  database.StoreID    `json:"store_id"`
	Key      string              `json:"key"`
	History  []providers.Message `json:"history,omitempty"`
	KeepLast int                 `json:"keep_last,omitempty"`
}

type sessionListRequest struct {
	StoreID database.StoreID `json:"store_id"`
	After   string           `json:"after,omitempty"`
	Limit   int              `json:"limit"`
}

type sessionMetaRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Key     string           `json:"key"`
	Scope   []byte           `json:"scope,omitempty"`
	Aliases []string         `json:"aliases,omitempty"`
}

type sessionSnapshotRequest struct {
	StoreID     database.StoreID            `json:"store_id"`
	Key         string                      `json:"key,omitempty"`
	Replacement *SessionSnapshotReplacement `json:"replacement,omitempty"`
}

type sessionDeleteRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Keys    []string         `json:"keys"`
}

type sessionMetaCASRequest struct {
	StoreID     database.StoreID `json:"store_id"`
	Key         string           `json:"key"`
	Expected    SessionMeta      `json:"expected"`
	Replacement *SessionMeta     `json:"replacement,omitempty"`
}

type sessionApplyMetaRequest struct {
	StoreID          database.StoreID `json:"store_id"`
	RequestedKey     string           `json:"requested_key"`
	CanonicalKey     string           `json:"canonical_key"`
	Existed          bool             `json:"existed"`
	Expected         SessionMeta      `json:"expected"`
	ExpectedRevision string           `json:"expected_revision,omitempty"`
	Replacement      SessionMeta      `json:"replacement"`
}

type sessionApplyAdmissionRequest struct {
	StoreID          database.StoreID             `json:"store_id"`
	RequestedKey     string                       `json:"requested_key"`
	CanonicalKey     string                       `json:"canonical_key"`
	Existed          bool                         `json:"existed"`
	Expected         SessionMeta                  `json:"expected"`
	ExpectedRevision string                       `json:"expected_revision,omitempty"`
	Decision         SessionMetaAdmissionDecision `json:"decision"`
}

type sessionBrokerResponse struct {
	OK           bool                     `json:"ok,omitempty"`
	Found        bool                     `json:"found,omitempty"`
	Changed      bool                     `json:"changed,omitempty"`
	Promoted     bool                     `json:"promoted,omitempty"`
	CanonicalKey string                   `json:"canonical_key,omitempty"`
	Summary      string                   `json:"summary,omitempty"`
	History      []providers.Message      `json:"history,omitempty"`
	Meta         SessionMeta              `json:"meta"`
	Revision     string                   `json:"revision,omitempty"`
	ModifiedAt   time.Time                `json:"modified_at,omitempty"`
	Sessions     []string                 `json:"sessions,omitempty"`
	Next         string                   `json:"next,omitempty"`
	State        SessionMetaMutationState `json:"state"`
}

// BrokerAdapter owns the local session store used by the composite
// session/thread broker handler. It is local-only even when a process client is
// installed, preventing recursive IPC.
type BrokerAdapter struct {
	dir      string
	store    *SQLiteStore
	storeID  database.StoreID
	selector string
	openOnce sync.Once
	openErr  error

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// NewBrokerAdapter resolves one logical sessions store exclusively from the
// broker's canonical home and validated configuration. Callers cannot submit a
// database directory or provider path.
func NewBrokerAdapter(
	home string,
	cfg *config.Config,
	storeID database.StoreID,
) (*BrokerAdapter, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedSessionsProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"session broker adapter requires authenticated broker authority",
		)
	}
	if !storeID.Valid() {
		return nil, database.NewError(database.CodeInvalid, "session StoreID is invalid")
	}
	dir, err := configuredSessionsDirectory(home, cfg, storeID)
	if err != nil {
		return nil, err
	}
	return newBrokerAdapterAtDirectory(dir, storeID)
}

func newBrokerAdapterAtDirectory(dir string, storeID database.StoreID) (*BrokerAdapter, error) {
	selector, err := WorkspaceSelector(dir)
	if err != nil {
		return nil, err
	}
	return &BrokerAdapter{dir: dir, storeID: storeID, selector: selector}, nil
}

// LocalStore exposes an already-opened stable typed store only to broker-side
// composition. It never opens provider storage.
func (adapter *BrokerAdapter) LocalStore() *SQLiteStore {
	if adapter == nil {
		return nil
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.store
}

// StoreID returns this adapter's opaque catalog identity.
func (adapter *BrokerAdapter) StoreID() database.StoreID {
	if adapter == nil {
		return ""
	}
	return adapter.storeID
}

// EnsureLocalStore opens this adapter's one retained provider pool on first
// typed use. Failure is isolated to this StoreID for the broker epoch.
func (adapter *BrokerAdapter) EnsureLocalStore() (*SQLiteStore, error) {
	if adapter == nil {
		return nil, database.NewError(database.CodeUnavailable, "session broker adapter is unavailable")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed {
		return nil, database.NewError(database.CodeUnavailable, "session broker adapter is closed")
	}
	return adapter.ensureLocalStoreLocked()
}

func (adapter *BrokerAdapter) ensureLocalStoreLocked() (*SQLiteStore, error) {
	adapter.openOnce.Do(func() {
		adapter.store, adapter.openErr = openLocalSQLiteStore(context.Background(), adapter.dir)
		if adapter.openErr != nil {
			adapter.openErr = mapSessionBrokerError(adapter.openErr)
			adapter.store = nil
			return
		}
		adapter.store.storeID = adapter.storeID
	})
	return adapter.store, adapter.openErr
}

func (adapter *BrokerAdapter) Handle(ctx context.Context, request database.Request) (any, error) {
	if adapter == nil || request.Domain != SessionsBrokerDomain ||
		request.Version != SessionsBrokerVersion || !strings.HasPrefix(request.Operation, "session.") {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "session request deadline was exceeded")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed {
		return nil, database.NewError(database.CodeUnavailable, "session broker adapter is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "session request deadline was exceeded")
	}
	if request.Operation == SessionOperationResolveStore {
		var input StoreResolutionRequest
		if request.DecodePayload(&input) != nil || input.WorkspaceSelector != adapter.selector {
			return nil, database.NewError(database.CodeUnauthorized, "session workspace is not cataloged")
		}
		return StoreResolutionResponse{StoreID: adapter.storeID}, nil
	}
	if _, err := adapter.ensureLocalStoreLocked(); err != nil {
		return nil, err
	}

	switch request.Operation {
	case sessionOperationPing:
		var input sessionStoreRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) {
			return nil, invalidSessionRequest()
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationAdd:
		var input sessionAddRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		if err := adapter.store.AddFullMessage(ctx, input.Key, input.Message); err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationHistory:
		var input sessionHistoryRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) || input.Offset < 0 || input.Limit < 1 ||
			input.Limit > sessionHistoryPageLimit {
			return nil, invalidSessionRequest()
		}
		history, err := adapter.store.GetHistory(ctx, input.Key)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		start := min(input.Offset, len(history))
		end := min(start+input.Limit, len(history))
		response := sessionBrokerResponse{History: cloneProviderMessages(history[start:end])}
		if end < len(history) {
			response.Next = strconv.Itoa(end)
		}
		return response, nil
	case sessionOperationGetSummary:
		var input sessionKeyRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		summary, err := adapter.store.GetSummary(ctx, input.Key)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Summary: summary}, nil
	case sessionOperationSetSummary:
		var input sessionSummaryRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) || len(input.Summary) >= maxLineSize ||
			!utf8.ValidString(input.Summary) {
			return nil, invalidSessionRequest()
		}
		if err := adapter.store.SetSummary(ctx, input.Key, input.Summary); err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationSetHistory, sessionOperationTruncate:
		var input sessionHistoryMutationRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		var err error
		if request.Operation == sessionOperationSetHistory {
			err = adapter.store.SetHistory(ctx, input.Key, input.History)
		} else {
			err = adapter.store.TruncateHistory(ctx, input.Key, input.KeepLast)
		}
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationCompact:
		var input sessionKeyRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) {
			return nil, invalidSessionRequest()
		}
		if err := adapter.store.Compact(ctx, input.Key); err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationList:
		var input sessionListRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			input.Limit < 1 || input.Limit > sessionListPageLimit {
			return nil, invalidSessionRequest()
		}
		keys := adapter.store.ListSessions()
		start := sort.SearchStrings(keys, input.After)
		if input.After != "" && start < len(keys) && keys[start] == input.After {
			start++
		}
		end := min(start+input.Limit, len(keys))
		response := sessionBrokerResponse{Sessions: append([]string(nil), keys[start:end]...)}
		if end < len(keys) && end > start {
			response.Next = keys[end-1]
		}
		return response, nil
	case sessionOperationResolve:
		var input sessionKeyRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		key, found, err := adapter.store.ResolveSessionKey(ctx, input.Key)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{CanonicalKey: key, Found: found}, nil
	case sessionOperationGetMeta:
		var input sessionKeyRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		meta, err := adapter.store.GetSessionMeta(ctx, input.Key)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Meta: cloneSessionMeta(meta)}, nil
	case sessionOperationUpsertMeta:
		var input sessionMetaRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		if err := adapter.store.UpsertSessionMeta(ctx, input.Key, input.Scope, input.Aliases); err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationReadState:
		var input sessionReadStateRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) || input.Offset < 0 || input.Limit < 1 ||
			input.Limit > sessionHistoryPageLimit {
			return nil, invalidSessionRequest()
		}
		key, history, meta, modified, found, err := adapter.store.ReadSessionStateStrict(ctx, input.Key)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		if input.ExpectedRevision != "" && input.ExpectedRevision != meta.Revision {
			return nil, database.NewError(database.CodeConflict, "session changed during pagination")
		}
		start := min(input.Offset, len(history))
		end := min(start+input.Limit, len(history))
		response := sessionBrokerResponse{
			CanonicalKey: key, History: cloneProviderMessages(history), Meta: cloneSessionMeta(meta),
			Revision: meta.Revision, ModifiedAt: modified, Found: found,
		}
		response.History = cloneProviderMessages(history[start:end])
		if end < len(history) {
			response.Next = strconv.Itoa(end)
		}
		return response, nil
	case sessionOperationReplaceSnapshot:
		var input sessionSnapshotRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			input.Replacement == nil {
			return nil, invalidSessionRequest()
		}
		if err := adapter.store.ReplaceSessionSnapshot(ctx, *input.Replacement); err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationPromoteAlias:
		var input sessionMetaRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		promoted, err := adapter.store.PromoteAliasHistory(ctx, input.Key, input.Scope, input.Aliases)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Promoted: promoted}, nil
	case sessionOperationEnsure:
		var input sessionKeyRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		if err := adapter.store.EnsureSessionHistory(ctx, input.Key); err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{OK: true}, nil
	case sessionOperationDelete:
		var input sessionDeleteRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) {
			return nil, invalidSessionRequest()
		}
		changed, err := adapter.store.DeleteSessions(ctx, input.Keys)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Changed: changed}, nil
	case sessionOperationCompareSwapMeta:
		var input sessionMetaCASRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		changed, err := adapter.store.CompareAndSwapSessionMetaStrict(
			ctx, input.Key, input.Expected, input.Replacement,
		)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Changed: changed}, nil
	case sessionOperationCompareDelete:
		var input sessionMetaCASRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		changed, err := adapter.store.CompareAndDeleteEmptySessionStrict(ctx, input.Key, input.Expected)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Changed: changed}, nil
	case sessionOperationReadMutationMeta:
		var input sessionKeyRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.Key) {
			return nil, invalidSessionRequest()
		}
		return adapter.readMutationMeta(ctx, input.Key)
	case sessionOperationApplyMeta:
		var input sessionApplyMetaRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.RequestedKey) || !validSessionKey(input.CanonicalKey) {
			return nil, invalidSessionRequest()
		}
		key, existed, err := adapter.store.UpdateSessionMetaStrict(
			ctx, input.RequestedKey,
			func(meta *SessionMeta, state SessionMetaMutationState) error {
				if state.SessionExists != input.Existed || state.MetadataExists != input.Existed ||
					meta.Key != input.Expected.Key || meta.Revision != input.ExpectedRevision {
					return ErrSnapshotConflict
				}
				*meta = cloneSessionMeta(input.Replacement)
				return nil
			},
		)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		if key != input.CanonicalKey || existed != input.Existed {
			return nil, database.NewError(database.CodeConflict, "session metadata changed")
		}
		return sessionBrokerResponse{Changed: true, CanonicalKey: key, Found: existed}, nil
	case sessionOperationApplyAdmission:
		var input sessionApplyAdmissionRequest
		if request.DecodePayload(&input) != nil || !adapter.validStoreID(input.StoreID) ||
			!validSessionKey(input.RequestedKey) || !validSessionKey(input.CanonicalKey) {
			return nil, invalidSessionRequest()
		}
		updated, err := adapter.store.AdmitSessionMeta(
			ctx, input.RequestedKey,
			func(meta SessionMeta, exists bool) (SessionMetaAdmissionDecision, error) {
				if exists != input.Existed || meta.Key != input.Expected.Key ||
					meta.Revision != input.ExpectedRevision {
					return SessionMetaAdmissionDecision{}, ErrSnapshotConflict
				}
				return input.Decision, nil
			},
		)
		if err != nil {
			return nil, mapSessionBrokerError(err)
		}
		return sessionBrokerResponse{Changed: updated}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "session operation is unsupported")
	}
}

var errReadMutationMeta = errors.New("memory: broker metadata read complete")

func (adapter *BrokerAdapter) readMutationMeta(
	ctx context.Context,
	requested string,
) (sessionBrokerResponse, error) {
	var response sessionBrokerResponse
	_, _, err := adapter.store.UpdateSessionMetaStrict(
		ctx, requested,
		func(meta *SessionMeta, state SessionMetaMutationState) error {
			response.CanonicalKey = requested
			if meta.Key != "" {
				response.CanonicalKey = meta.Key
			}
			response.Meta = cloneSessionMeta(*meta)
			response.Revision = meta.Revision
			response.Found = state.SessionExists
			response.State = state
			return errReadMutationMeta
		},
	)
	if !errors.Is(err, errReadMutationMeta) {
		return sessionBrokerResponse{}, mapSessionBrokerError(err)
	}
	return response, nil
}

func (adapter *BrokerAdapter) Close() error {
	if adapter == nil {
		return nil
	}
	adapter.closeOnce.Do(func() {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		adapter.closed = true
		if adapter.store != nil {
			adapter.closeErr = adapter.store.Close()
		}
	})
	return adapter.closeErr
}

func (s *SQLiteStore) pingBroker(ctx context.Context) error {
	var response sessionBrokerResponse
	if err := s.callSessionBroker(
		ctx, sessionOperationPing, sessionStoreRequest{StoreID: s.StoreID()}, &response, false,
	); err != nil {
		return err
	}
	if !response.OK {
		return database.NewError(database.CodeIntegrity, "session broker response is invalid")
	}
	return nil
}

func (s *SQLiteStore) callSessionBroker(
	ctx context.Context,
	operation string,
	input,
	output any,
	mutation bool,
) error {
	if s == nil || s.brokerClient == nil {
		return database.NewError(database.CodeUnavailable, "session broker client is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if mutation {
		return s.brokerClient.CallWithOptions(
			ctx, SessionsBrokerDomain, SessionsBrokerVersion, operation, input, output,
			database.CallOptions{Mutation: true},
		)
	}
	return s.brokerClient.Call(ctx, SessionsBrokerDomain, SessionsBrokerVersion, operation, input, output)
}

func (adapter *BrokerAdapter) validStoreID(id database.StoreID) bool {
	return adapter != nil && id == adapter.storeID && id.Valid()
}

// ResolveBrokerStoreID maps a constructor directory to a trusted StoreID
// without transmitting the path. The broker accepts only the opaque selector
// when it matches one workspace loaded from validated configuration.
func ResolveBrokerStoreID(
	ctx context.Context,
	client *database.Client,
	sessionsDir string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "session broker client is unavailable")
	}
	selector, err := WorkspaceSelector(sessionsDir)
	if err != nil {
		return "", err
	}
	var response StoreResolutionResponse
	err = client.Call(
		contextOrBackground(ctx), SessionsBrokerDomain, SessionsBrokerVersion,
		SessionOperationResolveStore,
		StoreResolutionRequest{WorkspaceSelector: selector}, &response,
	)
	if err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "session broker StoreID is invalid")
	}
	return response.StoreID, nil
}

// WorkspaceSelector derives a fixed opaque selector from a canonical
// constructor workspace. It cannot be reversed into a provider path.
func WorkspaceSelector(sessionsDir string) (string, error) {
	dir := strings.TrimSpace(sessionsDir)
	if dir == "" || strings.ContainsRune(dir, 0) {
		return "", database.NewError(database.CodeInvalid, "session workspace is invalid")
	}
	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "session workspace is invalid")
	}
	workspace := filepath.Dir(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(workspace); resolveErr == nil {
		workspace = resolved
	}
	digest := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return hex.EncodeToString(digest[:8]), nil
}

func configuredSessionsDirectory(
	home string,
	cfg *config.Config,
	storeID database.StoreID,
) (string, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	type candidate struct {
		workspace string
		primary   bool
	}
	primary, err := resolveConfiguredWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
	if err != nil {
		return "", err
	}
	candidates := []candidate{{workspace: primary, primary: true}}
	for _, agent := range cfg.Agents.List {
		if strings.TrimSpace(agent.Workspace) == "" {
			continue
		}
		workspace, resolveErr := resolveConfiguredWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return "", resolveErr
		}
		candidates = append(candidates, candidate{workspace: workspace})
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		if _, duplicate := seen[item.workspace]; duplicate {
			continue
		}
		seen[item.workspace] = struct{}{}
		candidateID := SessionsStoreID
		if !item.primary {
			selector, selectorErr := WorkspaceSelector(filepath.Join(item.workspace, "sessions"))
			if selectorErr != nil {
				return "", selectorErr
			}
			candidateID, selectorErr = database.ParseStoreID("workspace." + selector + ".sessions")
			if selectorErr != nil {
				return "", selectorErr
			}
		}
		if candidateID == storeID {
			return filepath.Join(item.workspace, "sessions"), nil
		}
	}
	return "", database.NewError(database.CodeUnauthorized, "session StoreID is not cataloged")
}

func resolveConfiguredWorkspace(home, configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = filepath.Join(home, "workspace")
	} else if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(home, value)
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "session workspace is invalid")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return abs, nil
}

func validSessionKey(key string) bool {
	return key != "" && key == strings.TrimSpace(key) && len(key) <= 4096 &&
		utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}

func invalidSessionRequest() error {
	return database.NewError(database.CodeInvalid, "session request is invalid")
}

func mapSessionBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "session operation failed")
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "session operation deadline was exceeded")
	case errors.Is(err, ErrSnapshotConflict):
		return database.NewError(database.CodeConflict, "session changed concurrently")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "session store schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrIntegrity), errors.Is(err, sqlitestore.ErrInvalidSchema):
		return database.NewError(database.CodeIntegrity, "session store integrity validation failed")
	default:
		return database.NewError(database.CodeInternal, "session operation failed")
	}
}

func cloneProviderMessages(messages []providers.Message) []providers.Message {
	result := make([]providers.Message, len(messages))
	for index := range messages {
		message := messages[index]
		if message.CreatedAt != nil {
			created := *message.CreatedAt
			message.CreatedAt = &created
		}
		message.Media = append([]string(nil), message.Media...)
		message.Attachments = append([]providers.Attachment(nil), message.Attachments...)
		message.Parts = append([]providers.PromptPart(nil), message.Parts...)
		message.SystemParts = append([]providers.ContentBlock(nil), message.SystemParts...)
		for blockIndex := range message.SystemParts {
			if message.SystemParts[blockIndex].CacheControl != nil {
				cache := *message.SystemParts[blockIndex].CacheControl
				message.SystemParts[blockIndex].CacheControl = &cache
			}
		}
		message.ToolCalls = append([]providers.ToolCall(nil), message.ToolCalls...)
		for callIndex := range message.ToolCalls {
			if message.ToolCalls[callIndex].Function != nil {
				function := *message.ToolCalls[callIndex].Function
				message.ToolCalls[callIndex].Function = &function
			}
			if message.ToolCalls[callIndex].ExtraContent != nil {
				extra := *message.ToolCalls[callIndex].ExtraContent
				if extra.Google != nil {
					google := *extra.Google
					extra.Google = &google
				}
				message.ToolCalls[callIndex].ExtraContent = &extra
			}
		}
		result[index] = message
	}
	return result
}
