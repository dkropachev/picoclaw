package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
)

const (
	// BrokerPreflightOperation opens and fully validates exactly one cataloged
	// sessions/thread store without mutating domain data.
	BrokerPreflightOperation = "preflight"

	threadOperationPing         = "thread.ping"
	threadOperationSearch       = "thread.search"
	threadOperationList         = "thread.list"
	threadOperationGet          = "thread.get"
	threadOperationGetMeta      = "thread.get-meta"
	threadOperationCreate       = "thread.create"
	threadOperationCreatePico   = "thread.create-pico"
	threadOperationUpdate       = "thread.update"
	threadOperationAttach       = "thread.attach"
	threadOperationDetach       = "thread.detach"
	threadOperationReturnOrigin = "thread.return-origin"

	threadBrokerPageLimit = MaxLimit
)

type threadStoreRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type threadListRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Options ListOptions      `json:"options"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

type threadSearchRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Options SearchOptions    `json:"options"`
}

type threadIDRequest struct {
	StoreID database.StoreID `json:"store_id"`
	ID      string           `json:"id"`
}

type threadCreateRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Request CreateRequest    `json:"request"`
}

type threadCreatePicoRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	Allocation PicoAllocation   `json:"allocation"`
	Request    CreateRequest    `json:"request"`
}

type threadUpdateRequest struct {
	StoreID database.StoreID `json:"store_id"`
	ID      string           `json:"id"`
	Request UpdateRequest    `json:"request"`
}

type threadAttachRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Request AttachRequest    `json:"request"`
}

type threadDetachRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	SessionKey string           `json:"session_key"`
}

type threadBrokerResponse struct {
	OK      bool           `json:"ok,omitempty"`
	Found   bool           `json:"found,omitempty"`
	Thread  *Thread        `json:"thread,omitempty"`
	Threads []Thread       `json:"threads,omitempty"`
	Meta    *ThreadMeta    `json:"meta,omitempty"`
	Handoff *ThreadHandoff `json:"handoff,omitempty"`
	Next    int            `json:"next,omitempty"`
}

type brokerWorkspace struct {
	workspace string
	adapter   *memory.BrokerAdapter
	store     Store
	once      sync.Once
	err       error
}

func (workspace *brokerWorkspace) ensureStore() (Store, error) {
	if workspace == nil || workspace.adapter == nil {
		return Store{}, database.NewError(database.CodeUnavailable, "session/thread store is unavailable")
	}
	workspace.once.Do(func() {
		local, err := workspace.adapter.EnsureLocalStore()
		if err != nil {
			workspace.err = err
			return
		}
		store := workspace.store
		store.brokerStore = local
		workspace.store = store
	})
	return workspace.store, workspace.err
}

// BrokerHandler composes one stable session adapter/pool for every cataloged
// configured workspace with the broker-side thread domain.
type BrokerHandler struct {
	workspaces map[database.StoreID]*brokerWorkspace
	selectors  map[string]database.StoreID

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	configured, err := configuredSessionWorkspaces(canonicalHome, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*brokerWorkspace, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	for _, item := range configured {
		adapter, configureErr := memory.NewBrokerAdapter(canonicalHome, cfg, item.storeID)
		if configureErr != nil {
			_ = handler.Close()
			return nil, configureErr
		}
		handler.workspaces[item.storeID] = &brokerWorkspace{
			workspace: item.workspace,
			adapter:   adapter,
			store:     newBrokerThreadStore(item.workspace, item.storeID),
		}
		handler.selectors[item.selector] = item.storeID
	}
	return handler, nil
}

func newBrokerThreadStore(workspace string, storeID database.StoreID) Store {
	workspace = ResolveWorkspace(workspace)
	return Store{
		Dir:              filepath.Join(workspace, "sessions"),
		Workspace:        workspace,
		ThreadsDir:       filepath.Join(workspace, "threads"),
		HandoffsDir:      filepath.Join(workspace, "threads", "handoffs"),
		brokerStoreID:    storeID,
		brokerResolveErr: nil,
	}
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != memory.SessionsBrokerDomain ||
		request.Version != memory.SessionsBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if request.Operation == memory.SessionOperationResolveStore {
		var input memory.StoreResolutionRequest
		if request.DecodePayload(&input) != nil || input.WorkspaceSelector == "" {
			return nil, invalidThreadRequest()
		}
		handler.mu.Lock()
		defer handler.mu.Unlock()
		if handler.closed {
			return nil, database.NewError(database.CodeUnavailable, "session/thread broker is closed")
		}
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "session workspace is not cataloged")
		}
		return memory.StoreResolutionResponse{StoreID: storeID}, nil
	}
	if request.Operation == BrokerPreflightOperation {
		var input threadStoreRequest
		if request.DecodePayload(&input) != nil || !input.StoreID.Valid() {
			return nil, invalidThreadRequest()
		}
		workspace := handler.workspaces[input.StoreID]
		if workspace == nil {
			return nil, database.NewError(database.CodeUnauthorized, "session StoreID is not cataloged")
		}
		handler.mu.Lock()
		defer handler.mu.Unlock()
		if handler.closed {
			return nil, database.NewError(database.CodeUnavailable, "session/thread broker is closed")
		}
		if _, err := workspace.ensureStore(); err != nil {
			return nil, mapThreadBrokerError(err)
		}
		return threadBrokerResponse{OK: true}, nil
	}
	storeID, err := requestStoreID(request)
	if err != nil {
		return nil, err
	}
	workspace, ok := handler.workspaces[storeID]
	if !ok {
		return nil, database.NewError(database.CodeUnauthorized, "session StoreID is not cataloged")
	}
	if strings.HasPrefix(request.Operation, "session.") {
		return workspace.adapter.Handle(ctx, request)
	}
	if !strings.HasPrefix(request.Operation, "thread.") {
		return nil, database.NewError(database.CodeUnsupported, "session/thread operation is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, database.NewError(database.CodeDeadline, "thread request deadline was exceeded")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "session/thread broker is closed")
	}
	store, err := workspace.ensureStore()
	if err != nil {
		return nil, mapThreadBrokerError(err)
	}

	switch request.Operation {
	case threadOperationPing:
		var input threadStoreRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) {
			return nil, invalidThreadRequest()
		}
		return threadBrokerResponse{OK: true}, nil
	case threadOperationSearch:
		var input threadSearchRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			input.Options.Offset < 0 || input.Options.Limit < 0 || input.Options.Limit > MaxLimit {
			return nil, invalidThreadRequest()
		}
		items, err := store.Search(input.Options)
		return threadBrokerResponse{Threads: cloneThreads(items)}, mapThreadBrokerError(err)
	case threadOperationList:
		var input threadListRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			input.Offset < 0 || input.Limit < 1 || input.Limit > threadBrokerPageLimit {
			return nil, invalidThreadRequest()
		}
		items, err := store.ListAll(input.Options)
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		start := min(input.Offset, len(items))
		end := min(start+input.Limit, len(items))
		response := threadBrokerResponse{Threads: cloneThreads(items[start:end])}
		if end < len(items) {
			response.Next = end
		}
		return response, nil
	case threadOperationGet:
		var input threadIDRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			!validThreadIdentity(input.ID) {
			return nil, invalidThreadRequest()
		}
		item, found, err := store.Get(input.ID)
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		response := threadBrokerResponse{Found: found}
		if found {
			threadCopy := cloneThread(item)
			response.Thread = &threadCopy
		}
		return response, nil
	case threadOperationGetMeta:
		var input threadIDRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			!validThreadIdentity(input.ID) {
			return nil, invalidThreadRequest()
		}
		meta, found, err := store.GetMeta(input.ID)
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		response := threadBrokerResponse{Found: found}
		if found {
			metaCopy := cloneThreadMeta(meta)
			response.Meta = &metaCopy
		}
		return response, nil
	case threadOperationCreate:
		var input threadCreateRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) {
			return nil, invalidThreadRequest()
		}
		item, err := store.CreateThread(ctx, cloneCreateRequest(input.Request))
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		threadCopy := cloneThread(item)
		return threadBrokerResponse{Found: true, Thread: &threadCopy}, nil
	case threadOperationCreatePico:
		var input threadCreatePicoRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) {
			return nil, invalidThreadRequest()
		}
		item, err := store.createPicoThreadWithAllocation(
			ctx, input.Allocation, cloneCreateRequest(input.Request),
		)
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		threadCopy := cloneThread(item)
		return threadBrokerResponse{Found: true, Thread: &threadCopy}, nil
	case threadOperationUpdate:
		var input threadUpdateRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			!validThreadIdentity(input.ID) {
			return nil, invalidThreadRequest()
		}
		item, found, err := store.UpdateThread(input.ID, cloneUpdateRequest(input.Request))
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		response := threadBrokerResponse{Found: found}
		if found {
			threadCopy := cloneThread(item)
			response.Thread = &threadCopy
		}
		return response, nil
	case threadOperationAttach:
		var input threadAttachRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) {
			return nil, invalidThreadRequest()
		}
		item, handoff, err := store.AttachCurrent(ctx, cloneAttachRequest(input.Request))
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		itemCopy, handoffCopy := cloneThread(item), handoff
		return threadBrokerResponse{Found: true, Thread: &itemCopy, Handoff: &handoffCopy}, nil
	case threadOperationDetach:
		var input threadDetachRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			!validThreadIdentity(input.SessionKey) {
			return nil, invalidThreadRequest()
		}
		if err := store.DetachCurrent(input.SessionKey); err != nil {
			return nil, mapThreadBrokerError(err)
		}
		return threadBrokerResponse{OK: true}, nil
	case threadOperationReturnOrigin:
		var input threadIDRequest
		if request.DecodePayload(&input) != nil || !validThreadStoreID(input.StoreID) ||
			!validThreadIdentity(input.ID) {
			return nil, invalidThreadRequest()
		}
		handoff, found, err := store.ReturnToOrigin(input.ID)
		if err != nil {
			return nil, mapThreadBrokerError(err)
		}
		response := threadBrokerResponse{Found: found}
		if found {
			handoffCopy := handoff
			response.Handoff = &handoffCopy
		}
		return response, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "thread operation is unsupported")
	}
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		handler.closed = true
		for _, workspace := range handler.workspaces {
			handler.closeErr = errors.Join(handler.closeErr, workspace.adapter.Close())
		}
	})
	return handler.closeErr
}

func validThreadStoreID(id database.StoreID) bool {
	return id.Valid()
}

func requestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if err := json.Unmarshal(request.Payload, &header); err != nil || !header.StoreID.Valid() {
		return "", invalidThreadRequest()
	}
	return header.StoreID, nil
}

type configuredSessionWorkspace struct {
	workspace string
	selector  string
	storeID   database.StoreID
}

func configuredSessionWorkspaces(
	home string,
	cfg *config.Config,
) ([]configuredSessionWorkspace, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	primary, err := resolveBrokerWorkspace(home, cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, err
	}
	inputs := []struct {
		workspace string
		primary   bool
	}{{workspace: primary, primary: true}}
	for _, agent := range cfg.Agents.List {
		if strings.TrimSpace(agent.Workspace) == "" {
			continue
		}
		workspace, resolveErr := resolveBrokerWorkspace(home, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inputs = append(inputs, struct {
			workspace string
			primary   bool
		}{workspace: workspace})
	}
	seenWorkspace := make(map[string]struct{}, len(inputs))
	seenSelector := make(map[string]string, len(inputs))
	result := make([]configuredSessionWorkspace, 0, len(inputs))
	for _, input := range inputs {
		if _, duplicate := seenWorkspace[input.workspace]; duplicate {
			continue
		}
		seenWorkspace[input.workspace] = struct{}{}
		selector, selectorErr := memory.WorkspaceSelector(ResolveSessionsDir(input.workspace))
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := seenSelector[selector]; collision && previous != input.workspace {
			return nil, database.NewError(database.CodeIntegrity, "session workspace selector collides")
		}
		seenSelector[selector] = input.workspace
		storeID := memory.SessionsStoreID
		if !input.primary {
			parsed, parseErr := database.ParseStoreID("workspace." + selector + ".sessions")
			if parseErr != nil {
				return nil, parseErr
			}
			storeID = parsed
		}
		result = append(result, configuredSessionWorkspace{
			workspace: input.workspace, selector: selector, storeID: storeID,
		})
	}
	return result, nil
}

func resolveBrokerWorkspace(home, configured string) (string, error) {
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
		return "", fmt.Errorf("resolve session workspace: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return abs, nil
}

func validThreadIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 4096 &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func invalidThreadRequest() error {
	return database.NewError(database.CodeInvalid, "thread request is invalid")
}

func mapThreadBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "thread operation failed")
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "thread operation deadline was exceeded")
	case errors.Is(err, os.ErrNotExist), errors.Is(err, errSessionMissing):
		return database.NewError(database.CodeNotFound, "thread resource was not found")
	case errors.Is(err, errReviewScope):
		return database.NewError(database.CodeUnauthorized, "review session cannot be used as a thread")
	default:
		return database.NewError(database.CodeInternal, "thread operation failed")
	}
}

func cloneThreads(items []Thread) []Thread {
	result := make([]Thread, len(items))
	for index := range items {
		result[index] = cloneThread(items[index])
	}
	return result
}

func cloneThread(item Thread) Thread {
	item.Context = cleanContext(item.Context)
	if item.DroppedAt != nil {
		dropped := *item.DroppedAt
		item.DroppedAt = &dropped
	}
	return item
}

func cloneThreadMeta(meta ThreadMeta) ThreadMeta {
	meta.Context = cleanContext(meta.Context)
	meta.SessionKeys = append([]string(nil), meta.SessionKeys...)
	meta.Aliases = append([]string(nil), meta.Aliases...)
	if meta.DroppedAt != nil {
		dropped := *meta.DroppedAt
		meta.DroppedAt = &dropped
	}
	return meta
}

func cloneCreateRequest(request CreateRequest) CreateRequest {
	request.Context = cleanContext(request.Context)
	request.SessionKeys = append([]string(nil), request.SessionKeys...)
	return request
}

func cloneUpdateRequest(request UpdateRequest) UpdateRequest {
	request.Context = cleanContext(request.Context)
	if request.Discoverable != nil {
		value := *request.Discoverable
		request.Discoverable = &value
	}
	return request
}

func cloneAttachRequest(request AttachRequest) AttachRequest {
	if request.Scope != nil {
		request.Scope = session.CloneScope(request.Scope)
	}
	return request
}

var _ database.Handler = (*BrokerHandler)(nil)
