package seahorse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const (
	BrokerDomain     = "seahorse"
	BrokerVersion    = 1
	seahorsePageSize = 200
	opResolveStore   = "resolve-store"
	opPreflight      = "preflight"
)

const (
	opGetOrCreate           = "get-or-create-conversation"
	opGetConversation       = "get-conversation"
	opGetSessionStatus      = "get-session-status"
	opGetStatuses           = "get-statuses"
	opAddMessage            = "add-message"
	opAddMessageParts       = "add-message-parts"
	opGetMessages           = "get-messages"
	opGetMessageCount       = "get-message-count"
	opGetMessage            = "get-message"
	opUpdateReasoning       = "update-reasoning"
	opUpdateModel           = "update-model"
	opUpdateCreated         = "update-created"
	opCreateSummary         = "create-summary"
	opGetSummary            = "get-summary"
	opGetSummaries          = "get-summaries"
	opGetSummaryChildren    = "get-summary-children"
	opGetSummaryParents     = "get-summary-parents"
	opLinkSummaryMessages   = "link-summary-messages"
	opGetSummaryMessages    = "get-summary-messages"
	opGetRoots              = "get-root-summaries"
	opGetContext            = "get-context"
	opUpsertContext         = "upsert-context"
	opClearContext          = "clear-context"
	opDeleteMessagesAfter   = "delete-messages-after"
	opClearConversation     = "clear-conversation"
	opAppendContextMessages = "append-context-messages"
	opAppendContextSummary  = "append-context-summary"
	opReplaceRange          = "replace-context-range"
	opReplaceItems          = "replace-context-items"
	opGetContextTokens      = "get-context-tokens"
	opGetMaxOrdinal         = "get-max-ordinal"
	opGetDepths             = "get-depths"
	opGetSubtree            = "get-subtree"
	opSearchSummaries       = "search-summaries"
	opSearchMessages        = "search-messages"
)

type seahorseRequest struct {
	StoreID             database.StoreID   `json:"store_id"`
	WorkspaceSelector   string             `json:"workspace_selector,omitempty"`
	SessionKey          string             `json:"session_key,omitempty"`
	ConvID              int64              `json:"conversation_id,omitempty"`
	MessageID           int64              `json:"message_id,omitempty"`
	MessageIDs          []int64            `json:"message_ids,omitempty"`
	Role                string             `json:"role,omitempty"`
	Content             string             `json:"content,omitempty"`
	ModelName           string             `json:"model_name,omitempty"`
	Reasoning           string             `json:"reasoning,omitempty"`
	Parts               []MessagePart      `json:"parts,omitempty"`
	TokenCount          int                `json:"token_count,omitempty"`
	CreatedAt           time.Time          `json:"created_at,omitempty"`
	Limit               int                `json:"limit,omitempty"`
	BeforeID            int64              `json:"before_id,omitempty"`
	Offset              int                `json:"offset,omitempty"`
	SummaryInput        CreateSummaryInput `json:"summary_input,omitempty"`
	SummaryID           string             `json:"summary_id,omitempty"`
	SummaryIDs          []string           `json:"summary_ids,omitempty"`
	ContextItems        []ContextItem      `json:"context_items,omitempty"`
	StartOrdinal        int                `json:"start_ordinal,omitempty"`
	EndOrdinal          int                `json:"end_ordinal,omitempty"`
	MaxOrdinalExclusive int                `json:"max_ordinal_exclusive,omitempty"`
	Search              SearchInput        `json:"search,omitempty"`
}

type seahorseStoreRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type seahorseResponse struct {
	StoreID      database.StoreID     `json:"store_id,omitempty"`
	Updated      bool                 `json:"updated,omitempty"`
	Conversation *Conversation        `json:"conversation,omitempty"`
	Status       *SessionStatus       `json:"status,omitempty"`
	Statuses     []SessionStatus      `json:"statuses,omitempty"`
	Message      *Message             `json:"message,omitempty"`
	Messages     []Message            `json:"messages,omitempty"`
	Count        int                  `json:"count,omitempty"`
	Summary      *Summary             `json:"summary,omitempty"`
	Summaries    []Summary            `json:"summaries,omitempty"`
	Strings      []string             `json:"strings,omitempty"`
	Context      []ContextItem        `json:"context,omitempty"`
	Depths       []int                `json:"depths,omitempty"`
	Subtree      []SummarySubtreeNode `json:"subtree,omitempty"`
	Search       []SearchResult       `json:"search,omitempty"`
	More         bool                 `json:"more,omitempty"`
}

type seahorseTarget struct {
	selector string
	path     string
}

type seahorseBrokerStore struct {
	target seahorseTarget
	engine *Engine
	opMu   sync.Mutex
}

func canonicalSeahorsePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("seahorse path invalid")
	}
	return filepath.Abs(filepath.Clean(path))
}

func seahorseWorkspaceSelector(workspace string) (string, error) {
	canonical, err := canonicalSeahorsePath(workspace)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:]), nil
}

func resolveSeahorseWorkspace(home, path string) (string, error) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") || path == "~" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = user
		} else {
			path = filepath.Join(user, path[2:])
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	return canonicalSeahorsePath(path)
}

func configuredSeahorseTargets(home string, cfg *config.Config) (map[database.StoreID]seahorseTarget, error) {
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return nil, err
	}
	result := map[database.StoreID]seahorseTarget{}
	add := func(id database.StoreID, workspace string) error {
		if !catalog.Contains(id) {
			return errors.New("seahorse target absent")
		}
		selector, selectorErr := seahorseWorkspaceSelector(workspace)
		if selectorErr != nil {
			return selectorErr
		}
		result[id] = seahorseTarget{
			selector: selector,
			path:     filepath.Join(workspace, "sessions", "seahorse.db"),
		}
		return nil
	}
	primary, err := canonicalSeahorsePath(cfg.WorkspacePath())
	if err != nil {
		return nil, err
	}
	if err := add("workspace.seahorse", primary); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{primary: {}}
	for _, agent := range cfg.Agents.List {
		if strings.TrimSpace(agent.Workspace) == "" {
			continue
		}
		workspace, err := resolveSeahorseWorkspace(home, agent.Workspace)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[workspace]; ok {
			continue
		}
		seen[workspace] = struct{}{}
		sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
		id, err := database.ParseStoreID("workspace." + hex.EncodeToString(sum[:8]) + ".seahorse")
		if err != nil {
			return nil, err
		}
		if err := add(id, workspace); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func resolveSeahorseBrokerStoreID(
	ctx context.Context,
	client *database.Client,
	workspace string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "seahorse broker client unavailable")
	}
	selector, err := seahorseWorkspaceSelector(workspace)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "seahorse workspace invalid")
	}
	var response seahorseResponse
	err = client.Call(
		ctx,
		BrokerDomain,
		BrokerVersion,
		opResolveStore,
		seahorseRequest{WorkspaceSelector: selector},
		&response,
	)
	if err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "seahorse broker StoreID invalid")
	}
	return response.StoreID, nil
}

func (s *Store) call(ctx context.Context, op string, in seahorseRequest, out *seahorseResponse, mutation bool) error {
	if !s.usesBroker() {
		return errors.New("seahorse broker unavailable")
	}
	in.StoreID = s.storeID
	var err error
	if mutation {
		err = s.broker.CallWithOptions(
			ctx,
			BrokerDomain,
			BrokerVersion,
			op,
			in,
			out,
			database.CallOptions{Mutation: true},
		)
	} else {
		err = s.broker.Call(ctx, BrokerDomain, BrokerVersion, op, in, out)
	}
	return decodeSeahorseError(err)
}

func paginate[T any](values []T, offset int) ([]T, bool) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(values) {
		offset = len(values)
	}
	end := offset + seahorsePageSize
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end], end < len(values)
}

func collect[T any](fetch func(int) ([]T, bool, error)) ([]T, error) {
	var result []T
	for offset := 0; ; offset += seahorsePageSize {
		values, more, err := fetch(offset)
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
		if !more {
			return result, nil
		}
	}
}

type BrokerHandler struct {
	mu        sync.RWMutex
	stores    map[database.StoreID]*seahorseBrokerStore
	selectors map[string]database.StoreID
	closed    bool
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedSeahorseProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"seahorse broker handler requires authenticated broker authority",
		)
	}
	if cfg == nil {
		return nil, database.NewError(database.CodeInvalid, "seahorse broker configuration invalid")
	}
	targets, err := configuredSeahorseTargets(home, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		stores:    make(map[database.StoreID]*seahorseBrokerStore, len(targets)),
		selectors: make(map[string]database.StoreID, len(targets)),
	}
	for id, target := range targets {
		if _, duplicate := handler.selectors[target.selector]; duplicate {
			return nil, database.NewError(database.CodeConflict, "seahorse workspace selector collision")
		}
		handler.stores[id] = &seahorseBrokerStore{target: target}
		handler.selectors[target.selector] = id
	}
	return handler, nil
}

func (h *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if h == nil || request.Domain != BrokerDomain || request.Version != BrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain unsupported")
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil, database.NewError(database.CodeUnavailable, "seahorse broker unavailable")
	}
	if request.Operation == opResolveStore {
		var input seahorseRequest
		if request.DecodePayload(&input) != nil || input.WorkspaceSelector == "" {
			return nil, database.NewError(database.CodeInvalid, "seahorse resolve request invalid")
		}
		storeID, ok := h.selectors[input.WorkspaceSelector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "seahorse workspace is not cataloged")
		}
		return seahorseResponse{StoreID: storeID}, nil
	}
	var in seahorseRequest
	if request.Operation == opPreflight {
		var target seahorseStoreRequest
		if err := request.DecodePayload(&target); err != nil {
			return nil, database.NewError(database.CodeInvalid, "seahorse request invalid")
		}
		in.StoreID = target.StoreID
	} else if err := request.DecodePayload(&in); err != nil {
		return nil, database.NewError(database.CodeInvalid, "seahorse request invalid")
	}
	item := h.stores[in.StoreID]
	if item == nil {
		return nil, database.NewError(database.CodeUnauthorized, "seahorse store not cataloged")
	}
	item.opMu.Lock()
	defer item.opMu.Unlock()
	if item.engine == nil {
		engine, openErr := newLocalEngine(Config{databasePath: item.target.path}, nil)
		if openErr != nil {
			return nil, mapSeahorseError(openErr)
		}
		item.engine = engine
	}
	engine := item.engine
	if request.Operation == opPreflight {
		return seahorseResponse{}, nil
	}
	out, err := dispatchSeahorse(ctx, engine.store, request.Operation, in)
	return out, mapSeahorseError(err)
}

func dispatchSeahorse(ctx context.Context, s *Store, op string, in seahorseRequest) (seahorseResponse, error) {
	switch op {
	case opGetOrCreate:
		v, e := s.GetOrCreateConversation(ctx, in.SessionKey)
		return seahorseResponse{Conversation: v}, e
	case opGetConversation:
		v, e := s.GetConversationBySessionKey(ctx, in.SessionKey)
		return seahorseResponse{Conversation: v}, e
	case opGetSessionStatus:
		v, e := s.GetSessionStatus(ctx, in.SessionKey)
		return seahorseResponse{Status: v}, e
	case opGetStatuses:
		v, e := s.GetAllSessionStatuses(ctx)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Statuses: p, More: m}, e
	case opAddMessage:
		v, e := s.AddMessageWithReasoning(
			ctx,
			in.ConvID,
			in.Role,
			in.Content,
			in.ModelName,
			in.Reasoning,
			in.TokenCount,
			in.CreatedAt,
		)
		return seahorseResponse{Message: v}, e
	case opAddMessageParts:
		v, e := s.AddMessageWithPartsAndReasoning(
			ctx,
			in.ConvID,
			in.Role,
			in.Parts,
			in.ModelName,
			in.Reasoning,
			in.TokenCount,
			in.CreatedAt,
		)
		return seahorseResponse{Message: v}, e
	case opGetMessages:
		v, e := s.GetMessages(ctx, in.ConvID, in.Limit, in.BeforeID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Messages: p, More: m}, e
	case opGetMessageCount:
		v, e := s.GetMessageCount(ctx, in.ConvID)
		return seahorseResponse{Count: v}, e
	case opGetMessage:
		v, e := s.GetMessageByID(ctx, in.MessageID)
		return seahorseResponse{Message: v}, e
	case opUpdateReasoning:
		return seahorseResponse{Updated: true}, s.UpdateMessageReasoningContent(ctx, in.MessageID, in.Reasoning)
	case opUpdateModel:
		return seahorseResponse{Updated: true}, s.UpdateMessageModelName(ctx, in.MessageID, in.ModelName)
	case opUpdateCreated:
		return seahorseResponse{Updated: true}, s.UpdateMessageCreatedAt(ctx, in.MessageID, in.CreatedAt)
	case opCreateSummary:
		v, e := s.CreateSummary(ctx, in.SummaryInput)
		return seahorseResponse{Summary: v}, e
	case opGetSummary:
		v, e := s.GetSummary(ctx, in.SummaryID)
		return seahorseResponse{Summary: v}, e
	case opGetSummaries:
		v, e := s.GetSummariesByConversation(ctx, in.ConvID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Summaries: p, More: m}, e
	case opGetSummaryChildren:
		v, e := s.GetSummaryChildren(ctx, in.SummaryID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Strings: p, More: m}, e
	case opGetSummaryParents:
		v, e := s.GetSummaryParents(ctx, in.SummaryID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Summaries: p, More: m}, e
	case opLinkSummaryMessages:
		return seahorseResponse{Updated: true}, s.LinkSummaryToMessages(ctx, in.SummaryID, in.MessageIDs)
	case opGetSummaryMessages:
		v, e := s.GetSummarySourceMessages(ctx, in.SummaryID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Messages: p, More: m}, e
	case opGetRoots:
		v, e := s.GetRootSummaries(ctx, in.ConvID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Summaries: p, More: m}, e
	case opGetContext:
		v, e := s.GetContextItems(ctx, in.ConvID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Context: p, More: m}, e
	case opUpsertContext:
		return seahorseResponse{Updated: true}, s.UpsertContextItems(ctx, in.ConvID, in.ContextItems)
	case opClearContext:
		return seahorseResponse{Updated: true}, s.ClearContextItems(ctx, in.ConvID)
	case opDeleteMessagesAfter:
		return seahorseResponse{Updated: true}, s.DeleteMessagesAfterID(ctx, in.ConvID, in.MessageID)
	case opClearConversation:
		return seahorseResponse{Updated: true}, s.ClearConversation(ctx, in.ConvID)
	case opAppendContextMessages:
		return seahorseResponse{Updated: true}, s.AppendContextMessages(ctx, in.ConvID, in.MessageIDs)
	case opAppendContextSummary:
		return seahorseResponse{Updated: true}, s.AppendContextSummary(ctx, in.ConvID, in.SummaryID)
	case opReplaceRange:
		return seahorseResponse{
				Updated: true,
			}, s.ReplaceContextRangeWithSummary(
				ctx,
				in.ConvID,
				in.StartOrdinal,
				in.EndOrdinal,
				in.SummaryID,
			)
	case opReplaceItems:
		return seahorseResponse{
				Updated: true,
			}, s.ReplaceContextItemsWithSummary(
				ctx,
				in.ConvID,
				in.SummaryIDs,
				in.SummaryID,
			)
	case opGetContextTokens:
		v, e := s.GetContextTokenCount(ctx, in.ConvID)
		return seahorseResponse{Count: v}, e
	case opGetMaxOrdinal:
		v, e := s.GetMaxOrdinal(ctx, in.ConvID)
		return seahorseResponse{Count: v}, e
	case opGetDepths:
		v, e := s.GetDistinctDepthsInContext(ctx, in.ConvID, in.MaxOrdinalExclusive)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Depths: p, More: m}, e
	case opGetSubtree:
		v, e := s.GetSummarySubtree(ctx, in.SummaryID)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Subtree: p, More: m}, e
	case opSearchSummaries:
		v, e := s.SearchSummaries(ctx, in.Search)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Search: p, More: m}, e
	case opSearchMessages:
		v, e := s.SearchMessages(ctx, in.Search)
		p, m := paginate(v, in.Offset)
		return seahorseResponse{Search: p, More: m}, e
	default:
		return seahorseResponse{}, database.NewError(database.CodeUnsupported, "seahorse operation unsupported")
	}
}

func mapSeahorseError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "seahorse operation failed")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return database.NewError(database.CodeDeadline, "seahorse deadline exceeded")
	}
	if sqliteprovider.IsInspectionIntegrity(err) {
		return database.NewError(database.CodeIntegrity, "seahorse integrity validation failed")
	}
	if errors.Is(err, os.ErrPermission) {
		return database.NewError(database.CodeUnavailable, "seahorse store unavailable")
	}
	return database.NewError(database.CodeInternal, "seahorse operation failed")
}

func decodeSeahorseError(err error) error {
	if err == nil {
		return nil
	}
	if database.CodeOf(err) == database.CodeOutcomeUnknown {
		return err
	}
	var value *database.Error
	if errors.As(err, &value) && value.Code == database.CodeDeadline {
		return context.DeadlineExceeded
	}
	return err
}

func (h *BrokerHandler) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	var result error
	for _, item := range h.stores {
		if item != nil && item.engine != nil {
			result = errors.Join(result, item.engine.Close())
		}
	}
	return result
}

var _ database.Handler = (*BrokerHandler)(nil)
